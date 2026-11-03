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

// normalizeTopP clamps a top_p value the gateway's gRPC frontend refuses.
//
// The OpenAI schema allows top_p anywhere in [0,1], and some clients spell
// "nucleus sampling off" as 0. The permissive gateway nodes ignore that value,
// but the gRPC frontend validates the range as (0,1] and answers HTTP 400:
//
//	[invalid_request_error] Invalid top_p value, the valid range of top_p is (0, 1.0]
//
// Because the node is chosen per attempt, the same client alternates between
// 200 and 400. Mapping 0 (and any negative or above-1 value) to 1.0 matches
// what the permissive nodes already do for 0: it disables nucleus sampling
// instead of narrowing it, so the accepted path keeps its current behaviour.
// Valid values and every other field are preserved; a body without the field
// is returned byte-for-byte.
func normalizeTopP(role roles.Role, body []byte) ([]byte, bool) {
	if role.Name != roles.Chat && role.Name != roles.Responses {
		return body, false
	}
	if !bytes.Contains(body, []byte(`"top_p"`)) {
		return body, false
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &object); errUnmarshal != nil {
		return body, false
	}
	raw, ok := object["top_p"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return body, false
	}
	var value float64
	if errDecode := json.Unmarshal(raw, &value); errDecode != nil {
		return body, false
	}
	if value > 0 && value <= 1 {
		return body, false
	}
	object["top_p"] = json.RawMessage("1")
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body, false
	}
	return out, true
}

// normalizeGeminiRoles gives every GenAI "contents" entry a role that the
// OpenCode Go Gemini frontend accepts.
//
// Google's GenAI schema makes Content.role optional: an omitted role means
// "user" (the official examples send the first user turn without a role). The
// OpenCode Go frontend instead decodes Content into a protobuf message whose
// role enum has no zero value it accepts, and answers HTTP 400 for a Content
// whose role is unset:
//
//	[400] Please use a valid role: user, model.
//
// The plugin is a byte pump for the GenAI face, so the repair belongs here:
// an omitted or empty role becomes "user" (the schema default), and the OpenAI
// spelling "assistant" becomes "model". The two legal GenAI roles and every
// other field are preserved byte-for-byte; a body without contents, a body that
// is not JSON, or a non-Gemini face is returned untouched.
func normalizeGeminiRoles(role roles.Role, body []byte) ([]byte, bool) {
	if role.Name != roles.Gemini {
		return body, false
	}
	if !bytes.Contains(body, []byte(`"contents"`)) {
		return body, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(trimmed, &object); errUnmarshal != nil {
		return body, false
	}
	rawContents, ok := object["contents"]
	if !ok {
		return body, false
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(rawContents), &items); errUnmarshal != nil {
		return body, false
	}
	changed := false
	for index, item := range items {
		var entry map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(item, &entry); errUnmarshal != nil {
			continue
		}
		wanted := ""
		value, hasRole := entry["role"]
		if !hasRole {
			wanted = "user"
		} else {
			var roleName string
			if errUnmarshal := json.Unmarshal(value, &roleName); errUnmarshal != nil {
				continue
			}
			switch roleName {
			case "user", "model":
				continue
			case "assistant":
				wanted = "model"
			case "":
				wanted = "user"
			default:
				// Any other spelling is a client error; do not silently
				// reinterpret a role the GenAI schema does not define.
				continue
			}
		}
		entry["role"] = json.RawMessage(`"` + wanted + `"`)
		encoded, errMarshal := json.Marshal(entry)
		if errMarshal != nil {
			continue
		}
		items[index] = encoded
		changed = true
	}
	if !changed {
		return body, false
	}
	encodedItems, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body, false
	}
	object["contents"] = encodedItems
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body, false
	}
	return out, true
}

// geminiSyntheticUserTurn is the continuation cue appended after a trailing
// model turn on the GenAI face.
//
// The turn is a single space on purpose: an empty text part is dropped before
// the gateway's turn validation runs (a body that ended with
// {"text":""} was still rejected as ending with a model turn), while a
// whitespace part survives and makes the model continue the prefilled turn
// instead of answering it again. A space is also the least intrusive cue: the
// model keeps writing from the prefill exactly as an assistant prefill request
// intends, whereas a word such as "continue" makes it restate the prefill.
var geminiSyntheticUserTurn = json.RawMessage(`{"role":"user","parts":[{"text":" "}]}`)

// normalizeGeminiTrailingUserTurn makes sure a GenAI "contents" array ends with
// a user turn.
//
// The OpenCode Go Gemini frontend rejects a request whose last Content is a
// model turn:
//
//	[400] Requests ending with a model turn are not supported.
//
// That requirement is a frontend limitation, not a verdict on the request:
// assistant prefill is legal in the OpenAI chat-completions world, and clients
// do send it (roleplay UIs send one or more assistant messages as a style
// primer). CLIProxyAPI translates those messages into trailing model turns and
// only strips a single trailing model turn, so a conversation with two or more
// consecutive trailing assistant messages reaches the gateway as an invalid
// body even though the key still has quota.
//
// The repair appends the synthetic user turn, which preserves the prefill and
// therefore the client's intended continuation semantics. A trailing model turn
// that carries tool results (functionResponse parts) is left alone: the gateway
// accepts it as the final turn and expects the model to answer it, so appending
// a cue would change the tool loop. Everything else in the body is preserved;
// a body without contents, a non-Gemini face, a last turn that is already user,
// a Content with an unknown role or malformed JSON is returned untouched.
func normalizeGeminiTrailingUserTurn(role roles.Role, body []byte) ([]byte, bool) {
	if role.Name != roles.Gemini {
		return body, false
	}
	if !bytes.Contains(body, []byte(`"contents"`)) {
		return body, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(trimmed, &object); errUnmarshal != nil {
		return body, false
	}
	rawContents, ok := object["contents"]
	if !ok {
		return body, false
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(rawContents), &items); errUnmarshal != nil {
		return body, false
	}
	if len(items) == 0 {
		return body, false
	}
	var last map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(items[len(items)-1], &last); errUnmarshal != nil {
		return body, false
	}
	var lastRole string
	if value, hasRole := last["role"]; hasRole {
		if errUnmarshal := json.Unmarshal(value, &lastRole); errUnmarshal != nil {
			return body, false
		}
	}
	if lastRole != "model" && lastRole != "assistant" {
		return body, false
	}
	if trailingModelHoldsToolResults(last["parts"]) {
		return body, false
	}
	items = append(items, geminiSyntheticUserTurn)
	encodedItems, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body, false
	}
	object["contents"] = encodedItems
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body, false
	}
	return out, true
}

// trailingModelHoldsToolResults reports whether a trailing model Content is the
// gateway's accepted shape for tool results: at least one functionResponse part
// and no inline media nested inside those responses.
//
// The media exception mirrors the host's own Gemini turn normalization: a model
// Content whose functionResponse carries inlineData/fileData is rejected as the
// final turn, so it still needs the synthetic user turn.
func trailingModelHoldsToolResults(rawParts json.RawMessage) bool {
	if len(bytes.TrimSpace(rawParts)) == 0 {
		return false
	}
	var parts []map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(rawParts, &parts); errUnmarshal != nil {
		return false
	}
	hasResponse := false
	hasMedia := false
	for _, part := range parts {
		rawResponse, ok := part["functionResponse"]
		if !ok {
			continue
		}
		hasResponse = true
		var response map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(rawResponse, &response); errUnmarshal != nil {
			continue
		}
		var nested []map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(response["parts"], &nested); errUnmarshal != nil {
			continue
		}
		for _, inner := range nested {
			for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
				if _, hasMediaPart := inner[key]; hasMediaPart {
					hasMedia = true
				}
			}
		}
	}
	return hasResponse && !hasMedia
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

// thinkingDisableFields are the top-level Chat Completions switches a client can
// use to ask for reasoning to be turned off. Different client families spell the
// switch differently, so the repair drops whichever one is present.
var thinkingDisableFields = []string{
	"reasoning_effort",
	"reasoning",
	"thinking",
	"enable_thinking",
	"reasoning_enabled",
}

// isThinkingDisableRejection reports whether an upstream 400 refuses a request
// because it asked a thinking-only model to stop reasoning.
//
// Some OpenCode Go model ids are thinking-only: GLM-5.3 always reasons, and the
// gateway answers any request that disables thinking with HTTP 400:
//
//	[400] GLM-5.3 is a thinking-only model; disabling thinking (reasoning_effort='none') is not supported.
//
// reasoning_effort is part of the OpenAI Chat Completions schema, so the client
// request is legal; the rejection is a model capability mismatch, not a quota,
// auth or content verdict. The caller repairs the body and retries once instead
// of surfacing a failure.
func isThinkingDisableRejection(body []byte) bool {
	lower := bytes.ToLower(body)
	if !bytes.Contains(lower, []byte("thinking-only")) {
		return false
	}
	for _, marker := range []string{
		"disabling thinking",
		"disable thinking",
		"disabling reasoning",
		"disable reasoning",
	} {
		if bytes.Contains(lower, []byte(marker)) {
			return true
		}
	}
	return false
}

// normalizeThinkingDisable drops top-level switches that ask a model to stop
// reasoning. It is the repair for isThinkingDisableRejection: a thinking-only
// model cannot honour them, and the gateway rejects the whole request when they
// are present. Removing them lets the model run in its only supported mode
// (thinking on) instead of failing the request.
//
// Switches that enable or tune reasoning are left untouched, as is every other
// field. A body without a disable switch, or a body that is not a JSON object,
// is returned byte-for-byte.
func normalizeThinkingDisable(role roles.Role, body []byte) ([]byte, bool) {
	if role.Name != roles.Chat && role.Name != roles.Responses {
		return body, false
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &object); errUnmarshal != nil {
		return body, false
	}
	changed := false
	for _, field := range thinkingDisableFields {
		raw, ok := object[field]
		if !ok {
			continue
		}
		if rawEnablesThinking(raw) {
			continue
		}
		delete(object, field)
		changed = true
	}
	if !changed {
		return body, false
	}
	out, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body, false
	}
	return out, true
}
