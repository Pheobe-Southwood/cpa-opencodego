package upstream

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

// normalizeDeveloperRoles rewrites the OpenAI "developer" message role to
// "system" for the OpenAI-shaped families.
//
// "developer" is a legal Chat Completions role (and the role newer OpenAI
// reasoning models use for the system prompt), but OpenCode Go's new inference
// router only accepts system/user/assistant/tool on some of its gateway nodes
// and answers HTTP 400 "chat message role `developer` is not supported by the
// router gRPC frontend". For every provider behind OpenCode Go the two roles
// carry the same meaning, so rewriting them leaves the model's behaviour
// unchanged while removing the intermittent 400.
//
// The rewrite is deliberately lazy: a body without the literal string is
// returned untouched, and an unmatched JSON body is never rewritten.
func normalizeDeveloperRoles(role roles.Role, body []byte, rewrite bool) []byte {
	if !rewrite {
		return body
	}
	if role.Name != roles.Chat && role.Name != roles.Responses {
		return body
	}
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &object); errUnmarshal != nil {
		return body
	}
	changed := false
	for _, field := range []string{"messages", "input"} {
		raw, ok := object[field]
		if !ok {
			continue
		}
		rewritten, ok := rewriteRoleArray(raw)
		if !ok {
			continue
		}
		object[field] = rewritten
		changed = true
	}
	if !changed {
		return body
	}
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body
	}
	return out
}

// rewriteRoleArray rewrites every item whose role is "developer" in one
// messages/input array and reports whether anything changed. Items that are not
// objects, or that carry an untouched role, are preserved verbatim.
func rewriteRoleArray(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return raw, false
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal(trimmed, &items); errUnmarshal != nil {
		return raw, false
	}
	changed := false
	for index, item := range items {
		var entry map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(item, &entry); errUnmarshal != nil {
			continue
		}
		value, ok := entry["role"]
		if !ok {
			continue
		}
		var roleName string
		if errUnmarshal := json.Unmarshal(value, &roleName); errUnmarshal != nil || roleName != "developer" {
			continue
		}
		entry["role"] = json.RawMessage(`"system"`)
		encoded, errMarshal := json.Marshal(entry)
		if errMarshal != nil {
			continue
		}
		items[index] = encoded
		changed = true
	}
	if !changed {
		return raw, false
	}
	out, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return raw, false
	}
	return out, true
}

// ensureReasoningContent fills reasoning_content on assistant history messages
// of a Chat Completions request that is in thinking mode.
//
// DeepSeek's thinking mode expects the reasoning_content that came back with
// every assistant message to be replayed on the next turn, including messages
// that also carry tool calls. A client is free to drop that history (DSH keeps
// it only for the in-flight turn), but some OpenCode Go gateway nodes enforce
// the rule literally and answer HTTP 400:
//
//	The `reasoning_content` in the thinking mode must be passed back to the API.
//
// The plugin cannot recover chain-of-thought the client threw away, so a
// neutral placeholder is written instead. Passing stale reasoning_content back
// is itself known to degrade long agentic runs (models imitate their old
// tool-call markup), so the placeholder is preferred over any attempt to
// reconstruct it. The field only has to be present for the request to be
// accepted; the upstream ignores it for generation.
//
// The rewrite keeps every other byte of the body intact: only the affected
// messages and the messages array are re-encoded. A body that is not JSON, or
// that is not in thinking mode under "auto", is returned untouched.
func ensureReasoningContent(role roles.Role, body []byte, mode string) []byte {
	if role.Name != roles.Chat || mode == config.ReasoningReplayOff || mode == config.ReasoningReplayRetryOnly {
		return body
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(trimmed, &object); errUnmarshal != nil {
		return body
	}
	rawMessages, ok := object["messages"]
	if !ok {
		return body
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(rawMessages), &items); errUnmarshal != nil {
		return body
	}

	thinking := mode == config.ReasoningReplayAlways || requestThinkingMode(object)
	entries := make([]map[string]json.RawMessage, len(items))
	assistants := make([]int, 0, len(items))
	for index, item := range items {
		var entry map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(item, &entry); errUnmarshal != nil {
			continue
		}
		entries[index] = entry
		var roleName string
		if value, hasRole := entry["role"]; hasRole {
			_ = json.Unmarshal(value, &roleName)
		}
		if roleName != "assistant" {
			continue
		}
		assistants = append(assistants, index)
		// A client that already replays reasoning for some turns is in thinking
		// mode even when it omits the reasoning_effort switch.
		if !thinking && hasTextReasoning(entry) {
			thinking = true
		}
	}
	if len(assistants) == 0 || !thinking {
		return body
	}

	placeholder, errPlaceholder := json.Marshal(config.DefaultReasoningPlaceholder)
	if errPlaceholder != nil {
		return body
	}
	changed := false
	for _, index := range assistants {
		entry := entries[index]
		if hasTextReasoning(entry) {
			continue
		}
		entry["reasoning_content"] = placeholder
		encoded, errMarshal := json.Marshal(entry)
		if errMarshal != nil {
			continue
		}
		items[index] = encoded
		changed = true
	}
	if !changed {
		return body
	}
	encodedItems, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	object["messages"] = encodedItems
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body
	}
	return out
}

// hasTextReasoning reports whether an assistant message already carries a
// non-empty reasoning_content. An empty string counts as missing because the
// gateway's proto3 frontend cannot tell it from an absent field.
func hasTextReasoning(entry map[string]json.RawMessage) bool {
	value, ok := entry["reasoning_content"]
	if !ok {
		return false
	}
	var text string
	if errUnmarshal := json.Unmarshal(value, &text); errUnmarshal != nil {
		return false
	}
	return strings.TrimSpace(text) != ""
}

// requestThinkingMode reports whether the top-level request asks for thinking.
// Only fields that actually exist on Chat Completions bodies are consulted, so
// a tool schema that happens to be called "reasoning" cannot trip the rewrite.
func requestThinkingMode(object map[string]json.RawMessage) bool {
	for _, field := range []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "reasoning_enabled"} {
		raw, ok := object[field]
		if !ok {
			continue
		}
		if rawEnablesThinking(raw) {
			return true
		}
	}
	return false
}

// rawEnablesThinking interprets one thinking switch. Strings such as "none" and
// "off", the booleans false, the number 0, and objects that explicitly disable
// thinking count as disabled; everything else counts as enabled.
func rawEnablesThinking(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case 't':
		return bytes.Equal(trimmed, []byte("true"))
	case 'f', 'n':
		return false
	case '{':
		var fields map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(trimmed, &fields); errUnmarshal != nil {
			return true
		}
		for _, key := range []string{"type", "enabled"} {
			value, ok := fields[key]
			if !ok {
				continue
			}
			var flag bool
			if errUnmarshal := json.Unmarshal(value, &flag); errUnmarshal == nil {
				return flag
			}
			var text string
			if errUnmarshal := json.Unmarshal(value, &text); errUnmarshal == nil {
				switch strings.ToLower(strings.TrimSpace(text)) {
				case "disabled", "off", "none", "false":
					return false
				default:
					return true
				}
			}
		}
		if effort, ok := fields["effort"]; ok {
			return rawEnablesThinking(effort)
		}
		return true
	case '[':
		return false
	}
	var text string
	if errUnmarshal := json.Unmarshal(trimmed, &text); errUnmarshal == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "", "none", "off", "false", "no", "disabled", "0":
			return false
		default:
			return true
		}
	}
	var number float64
	if errUnmarshal := json.Unmarshal(trimmed, &number); errUnmarshal == nil {
		return number > 0
	}
	return false
}

// isDeveloperRoleRejection reports whether an upstream rejection is caused by
// the gateway not accepting the Chat Completions "developer" role.
//
// The gateways behind OpenCode Go phrase the same rejection differently:
//   - "chat message role `developer` is not supported by the router gRPC frontend"
//   - "messages[0].role: unknown variant `developer`" (serde, often HTTP 422)
//   - "developer is not one of ['system', 'assistant', 'user', 'tool']"
//
// Matching only the first wording left the other two as non-repairable client
// errors even though the request was a legal Chat Completions request.
func isDeveloperRoleRejection(body []byte) bool {
	if !bytes.Contains(body, []byte("developer")) {
		return false
	}
	lower := bytes.ToLower(body)
	for _, marker := range []string{
		"not supported",
		"unknown variant",
		"is not one of",
		"unsupported role",
		"invalid role",
	} {
		if bytes.Contains(lower, []byte(marker)) {
			return true
		}
	}
	return false
}

// isTransientRejection reports whether an upstream 400 looks like a gateway
// glitch rather than a verdict on the request itself.
//
// Two shapes are observed on the OpenCode Go router:
//   - a bare `{"model":"..."}` body with no error object at all;
//   - "This response_format type is unavailable now" for json_schema requests
//     that succeed when retried on another gateway node.
//
// Both are retried once without changing the request body.
func isTransientRejection(body []byte) bool {
	if bytes.Contains(bytes.ToLower(body), []byte("response_format type is unavailable")) {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(trimmed, &object); errUnmarshal != nil {
		return false
	}
	if _, hasError := object["error"]; hasError {
		return false
	}
	_, hasModel := object["model"]
	return hasModel && len(object) <= 2
}

// isReasoningReplayRejection reports whether an upstream 400 is the thinking
// mode reasoning-passback check. The gateway answers it sporadically: the same
// bytes can succeed on the next attempt, so the caller retries once.
func isReasoningReplayRejection(body []byte) bool {
	if !bytes.Contains(body, []byte("must be passed back")) {
		return false
	}
	return bytes.Contains(body, []byte("reasoning_content")) || bytes.Contains(body, []byte("reasoning_text"))
}
