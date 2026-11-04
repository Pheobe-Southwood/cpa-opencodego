package plugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/reqlog"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/stats"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/upstream"
)

// executorStreamRequest mirrors the host's rpcExecutorRequest wrapper: the
// executor request fields plus the host-owned stream id.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// hopByHopHeaders must never be forwarded downstream.
var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"content-length":      {},
	"content-encoding":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"set-cookie":          {},
}

// extractCredential pulls the APIKey and Name from the host-injected auth payload.
func extractCredential(authID string, storageJSON []byte, authMetadata map[string]any, settings config.Settings) config.Key {
	var key config.Key
	if len(storageJSON) > 0 {
		var acc struct {
			Name    string `json:"name"`
			KeyName string `json:"key_name"`
			APIKey  string `json:"api_key"`
		}
		if err := json.Unmarshal(storageJSON, &acc); err == nil {
			if strings.TrimSpace(acc.APIKey) != "" {
				name := acc.KeyName
				if name == "" {
					name = acc.Name
				}
				return config.Key{
					Name: strings.TrimSpace(name),
					Key:  strings.TrimSpace(acc.APIKey),
				}
			}
		}
	}
	if authMetadata != nil {
		if apiKey, ok := authMetadata["api_key"].(string); ok && strings.TrimSpace(apiKey) != "" {
			name, _ := authMetadata["key_name"].(string)
			if name == "" {
				name, _ = authMetadata["name"].(string)
			}
			return config.Key{
				Name: strings.TrimSpace(name),
				Key:  strings.TrimSpace(apiKey),
			}
		}
	}
	// Fallback to configured keys by matching AuthID if possible
	if authID != "" {
		for _, k := range settings.Keys {
			if k.Name != "" && strings.Contains(authID, k.Name) {
				return k
			}
		}
	}
	// A single-key deployment stays usable even before the host's auth
	// projection has loaded (or when the host calls the executor without an
	// auth). With more than one key the plugin must not choose: CPA's
	// AuthManager owns that decision.
	if enabled := settings.EnabledKeys(); len(enabled) == 1 {
		return enabled[0]
	}
	return key
}

func (r *Runtime) handleExecute(request []byte) []byte {
	r.refreshShared()
	var executeRequest pluginapi.ExecutorRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &executeRequest); errUnmarshal != nil {
			return abi.Fail("invalid_request", "解析执行请求失败: "+errUnmarshal.Error(), http.StatusBadRequest)
		}
	}

	settings := r.Settings()
	requested := bareModelName(executeRequest.Model)
	cred := extractCredential(executeRequest.AuthID, executeRequest.StorageJSON, executeRequest.AuthMetadata, settings)

	attempt := r.newAttempt(requested, r.Resolver().Upstream(requested), false, cred,
		resolveSession(settings, executeRequest.Metadata))
	call := upstream.Call{
		Role:           r.role,
		Model:          attempt.upstream,
		RequestedModel: requested,
		Stream:         executeRequest.Stream,
		Body:           executeRequest.Payload,
		Session:        attempt.session,
		UserAgent:      inboundUserAgent(executeRequest.Headers),
		Credential:     cred,
	}

	started := time.Now()
	result, errExecute := r.client.Execute(context.Background(), call)
	if errExecute != nil {
		attempt.repairs = result.Repairs
		r.recordAttempt(attempt, started, result.Headers, reqlog.Usage{}, abi.Status(errExecute), errExecute.Error(), 0, 0, "error", false, false)
		return abi.FailFor(errExecute, "upstream_error")
	}

	usage := reqlog.ParseBody(r.role.Name, result.Body)
	payload := result.Body
	if r.role.Name == roles.Responses && strings.EqualFold(strings.TrimSpace(executeRequest.Format), "codex") {
		payload = terminalEventPayload(payload)
	}
	attempt.repairs = result.Repairs
	r.recordAttempt(attempt, started, result.Headers, usage, result.StatusCode, "", 0, 0, "stop", false, false)

	raw, errMarshal := abi.OK(pluginapi.ExecutorResponse{
		Payload: payload,
		Headers: forwardHeaders(result.Headers),
	})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

func (r *Runtime) handleExecuteStream(request []byte) []byte {
	r.refreshShared()
	var executeRequest executorStreamRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &executeRequest); errUnmarshal != nil {
			return abi.Fail("invalid_request", "解析流式执行请求失败: "+errUnmarshal.Error(), http.StatusBadRequest)
		}
	}

	settings := r.Settings()
	requested := bareModelName(executeRequest.Model)
	cred := extractCredential(executeRequest.AuthID, executeRequest.StorageJSON, executeRequest.AuthMetadata, settings)

	attempt := r.newAttempt(requested, r.Resolver().Upstream(requested), true, cred,
		resolveSession(settings, executeRequest.Metadata))
	call := upstream.Call{
		Role:           r.role,
		Model:          attempt.upstream,
		RequestedModel: requested,
		Stream:         true,
		Body:           executeRequest.Payload,
		Session:        attempt.session,
		UserAgent:      inboundUserAgent(executeRequest.Headers),
		Credential:     cred,
	}

	r.trace("execute_stream start", map[string]any{
		"role": string(r.role.Name), "requested": requested, "upstream": call.Model,
		"stream_id": executeRequest.StreamID, "payload_bytes": len(call.Body), "session": call.Session,
		"key": cred.Name,
	})

	started := time.Now()
	stream, errStream := r.client.Stream(context.Background(), call)
	if errStream != nil {
		r.trace("execute_stream upstream error", map[string]any{"model": call.Model, "error": errStream.Error()})
		r.recordAttempt(attempt, started, nil, reqlog.Usage{}, abi.Status(errStream), errStream.Error(), 0, 0, "error", false, false)
		return abi.FailFor(errStream, "upstream_error")
	}
	r.trace("execute_stream upstream ok", map[string]any{"model": call.Model, "status": stream.StatusCode})

	attempt.repairs = stream.Repairs
	headers := forwardHeaders(stream.Headers)
	streamID := strings.TrimSpace(executeRequest.StreamID)
	if streamID != "" {
		// The host keeps the downstream stream open while we emit asynchronously,
		// which preserves time to first token instead of buffering every chunk.
		go r.pumpStream(stream, streamID, streamTiming{
			model:   attempt.model,
			key:     attempt.key,
			started: started,
			attempt: attempt,
		})
	} else {
		// Without a stream id there is nowhere to push chunks, so fall back to
		// reading the upstream response to completion.
		payload, _ := io.ReadAll(stream.Body)
		_ = stream.Body.Close()
		r.recordAttempt(attempt, started, stream.Headers, reqlog.ParseBody(r.role.Name, payload),
			stream.StatusCode, "", 0, 0, "stop", false, false)
		raw, errMarshal := abi.OK(map[string]any{
			"headers": headers,
			"chunks":  []pluginapi.ExecutorStreamChunk{{Payload: payload}},
		})
		if errMarshal != nil {
			return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
		}
		return raw
	}

	raw, errMarshal := abi.OK(map[string]any{"headers": headers})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

// streamTiming is the per-request state used to measure TTFT and generation
// time. The host's usage record for this async stream bridge has no TTFT, so
// the plugin times the frames it reads itself.
type streamTiming struct {
	model   string
	key     string
	started time.Time
	// attempt carries the request-log metadata; tests may leave it zero, in
	// which case the log row is derived from model/key.
	attempt requestAttempt
}

// requestAttempt is the per-request metadata shared by the non-stream and
// stream paths.
type requestAttempt struct {
	role      roles.Name
	requested string
	upstream  string
	model     string
	key       string
	session   string
	stream    bool
	repairs   []string
}

// statsModelID maps the client-facing model id to the id the usage handler
// stores. The resolver returns the configured base id for an alias; the
// upstream id is the fallback for an id the resolver does not know.
func statsModelID(r *Runtime, requested, upstream string) string {
	if entry, ok := r.Resolver().Resolve(requested); ok && entry.BaseID != "" {
		return entry.BaseID
	}
	if strings.TrimSpace(upstream) != "" {
		return strings.TrimSpace(upstream)
	}
	return strings.TrimSpace(requested)
}

// pumpStream forwards upstream SSE frames to the host stream bridge, reshaping
// them into the chunk convention the target protocol handler expects.
func (r *Runtime) pumpStream(stream *upstream.Stream, streamID string, timing streamTiming) {
	defer func() { _ = stream.Body.Close() }()

	payloadStyle := r.role.SSEStyle() == roles.SSEPayload
	reader := bufio.NewReaderSize(stream.Body, 64*1024)
	accumulator := reqlog.NewAccumulator(r.role.Name)
	closeMessage := ""
	emitted := 0
	streamError := false
	canceled := false
	firstContent := time.Time{}
	lastContent := time.Time{}
	finishKind := ""
	finishSeen := false
	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			chunk, keep := streamChunk(r.role, line, payloadStyle)
			if keep {
				now := time.Now()
				if stats.IsStreamError(r.role, chunk) {
					streamError = true
				}
				if stats.IsContent(r.role, chunk) {
					if firstContent.IsZero() {
						firstContent = now
					}
					lastContent = now
				}
				if kind, ok := stats.StreamFinish(r.role, chunk); ok {
					finishSeen = true
					finishKind = kind
				}
				accumulator.Observe(chunk)
				if errEmit := emitStreamChunk(streamID, chunk); errEmit != nil {
					closeMessage = errEmit.Error()
					canceled = downstreamClosed(errEmit)
					break
				}
				emitted++
			}
		}
		if errRead != nil {
			if errRead != io.EOF {
				closeMessage = errRead.Error()
			}
			break
		}
	}
	incomplete := false
	if closeMessage == "" && !streamError {
		if !finishSeen && !firstContent.IsZero() {
			incomplete = true
			finishKind = "eof"
		}
	} else if streamError {
		finishKind = "error"
	} else if canceled {
		finishKind = "canceled"
	} else if closeMessage != "" {
		finishKind = "error"
	}
	if streamError {
		// An upstream error frame is authoritative: if the client disconnected
		// only after the upstream reported an error, the row stays a failure.
		canceled = false
	}
	ttft, generation := streamTimingValues(timing.started, firstContent, lastContent)
	attempt := timing.attempt
	if attempt.model == "" {
		attempt = requestAttempt{
			role:      r.role.Name,
			requested: timing.model,
			upstream:  timing.model,
			model:     timing.model,
			key:       timing.key,
			stream:    true,
		}
	}
	errorText := closeMessage
	if errorText == "" && streamError {
		errorText = "upstream stream error frame"
	}
	if canceled {
		// The host closed the stream because the downstream client went away (or
		// the plugin instance is being unloaded). The upstream did not fail, so
		// the row keeps the measured timings but is not recorded as an error.
		errorText = ""
	}
	r.recordAttempt(attempt, timing.started, stream.Headers, accumulator.Usage(),
		stream.StatusCode, errorText, ttft, generation, finishKind, incomplete, canceled)
	r.trace("stream finished", map[string]any{
		"role":       string(r.role.Name),
		"stream_id":  streamID,
		"chunks":     emitted,
		"close_note": closeMessage,
		"stream_err": streamError,
		"canceled":   canceled,
		"ttft_ms":    float64(ttft.Microseconds()) / 1000,
		"gen_ms":     float64(generation.Microseconds()) / 1000,
	})
	// A stream with no content, an upstream error frame, a broken transport, or
	// a downstream cancel has no meaningful TTFT. Failed/partial streams must
	// not lower the average.
	if closeMessage == "" && !streamError && !canceled && timing.model != "" && ttft > 0 {
		if store := r.statsStore(); store != nil {
			store.ObserveTiming(stats.TimingSample{
				Model:      timing.model,
				Family:     string(r.role.Name),
				Key:        timing.key,
				At:         timing.started,
				TTFT:       ttft,
				Generation: generation,
			})
		}
	}
	closeNote := closeMessage
	if canceled {
		closeNote = ""
	}
	closeStream(streamID, closeNote)
}

// streamTimingValues turns the content timestamps into TTFT and generation
// time. There is deliberately no first-frame fallback: a stream that never
// produced content is not a TTFT sample. Generation ends at the last content
// frame, so framed protocols' blank/control/[DONE] lines cannot stretch it.
func streamTimingValues(started, firstContent, lastContent time.Time) (time.Duration, time.Duration) {
	if started.IsZero() || firstContent.IsZero() {
		return 0, 0
	}
	ttft := firstContent.Sub(started)
	if ttft < 0 {
		ttft = 0
	}
	generation := lastContent.Sub(firstContent)
	if generation < 0 {
		generation = 0
	}
	return ttft, generation
}

// streamChunk converts one upstream line into the shape the host expects. For
// payload-style roles the "data: " prefix is removed because the host frames
// each chunk itself; the upstream's terminal sentinel is dropped since the host
// emits its own.
func streamChunk(role roles.Role, line []byte, payloadStyle bool) ([]byte, bool) {
	if !payloadStyle {
		return line, true
	}
	trimmed := bytes.TrimRight(line, "\r\n")
	trimmed = bytes.TrimSpace(trimmed)
	if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) {
		return nil, false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}

type streamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type streamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func emitStreamChunk(streamID string, payload []byte) error {
	return abi.CallHost(pluginabi.MethodHostStreamEmit, streamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	}, nil)
}

func closeStream(streamID, message string) {
	_ = abi.CallHost(pluginabi.MethodHostStreamClose, streamCloseRequest{
		StreamID: streamID,
		Error:    message,
	}, nil)
}

// downstreamClosed reports whether a host stream callback failed because the
// host closed the stream underneath the plugin. The host binds the stream to
// the downstream request context: when the client disconnects, or the plugin
// instance is unloaded during a host restart, the stream id is removed and the
// next emit returns "stream N is not open" / "plugin callback instance is
// closed" (or the callback context's own cancellation error). The upstream was
// healthy in all of those cases, so they must not be recorded as failures.
func downstreamClosed(err error) bool {
	if err == nil {
		return false
	}
	return downstreamClosedMessage(err.Error())
}

// downstreamClosedMessage matches the host's stream-bridge cancellation
// messages. It is shared with the request-log read path so rows written before
// this classification existed are repaired on read.
func downstreamClosedMessage(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" {
		return false
	}
	for _, marker := range []string{
		"is not open",                 // stream bridge: "stream 609 is not open"
		"callback instance is closed", // plugin instance unloaded by the host
		"context canceled",            // host callback context canceled
		"client closed request",       // HTTP 499 wording
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// terminalEventPayload wraps a bare OpenAI Responses body in the terminal
// event shape the host's codex non-stream translators require.
//
// Those translators only accept event payloads ({"type":"response.completed",
// "response":{...}}); given a raw response object they return an empty body, so
// a non-streaming client would receive HTTP 200 with no content. The built-in
// codex executor never hits this because it always streams upstream, so the
// plugin adapts to the same contract.
func terminalEventPayload(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	var probe struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if errUnmarshal := json.Unmarshal(trimmed, &probe); errUnmarshal != nil {
		return body
	}
	if probe.Type != "" {
		// Already an event payload.
		return body
	}
	eventType := "response.completed"
	if probe.Status == "incomplete" {
		eventType = "response.incomplete"
	}
	wrapped, errMarshal := json.Marshal(map[string]json.RawMessage{
		"type":     json.RawMessage(strconv.Quote(eventType)),
		"response": json.RawMessage(trimmed),
	})
	if errMarshal != nil {
		return body
	}
	return wrapped
}

// resolveSession picks the value for the x-opencode-session header. OpenCode
// documents it as "a stable session id per conversation"; the host publishes
// exactly that in the request metadata, and a fixed value is used as fallback.
func resolveSession(settings config.Settings, metadata map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(settings.SessionMode), "auto") {
		for _, key := range []string{"canonical_session_id", "execution_session_id", "parent_session_id"} {
			if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return settings.Session
}

// bareModelName strips the thinking-effort suffix, mirroring the host's own
// thinking.ParseSuffix so the upstream receives the bare model id.
func bareModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	lastOpen := strings.LastIndex(trimmed, "(")
	if lastOpen <= 0 || !strings.HasSuffix(trimmed, ")") {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:lastOpen])
}

// forwardHeaders drops hop-by-hop and encoding headers so the translated
// downstream response is not corrupted.
func forwardHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	out := make(http.Header, len(headers))
	for name, values := range headers {
		if _, skip := hopByHopHeaders[strings.ToLower(name)]; skip {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}

// trace appends a diagnostic line to <state-dir>/debug.log. It is a no-op
// unless the debug setting is enabled; a plain file keeps diagnostics off the
// host callback path so it can never disturb a live stream.
func (r *Runtime) trace(message string, fields map[string]any) {
	if !r.Debug() {
		return
	}
	r.mu.Lock()
	path := r.debugPath
	r.mu.Unlock()
	if path == "" {
		return
	}
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + message
	if len(fields) > 0 {
		encoded, errMarshal := json.Marshal(fields)
		if errMarshal == nil {
			line += " " + string(encoded)
		}
	}
	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.WriteString(line + "\n")
}

// newAttempt builds the request-log metadata shared by both execution paths.
// The key comes from the host-selected credential; when CPA did not name one
// (only possible with a single-key configuration) extractCredential has already
// filled it in, so an empty key here means no key was used at all.
func (r *Runtime) newAttempt(requested, upstreamModel string, stream bool, cred config.Key, session string) requestAttempt {
	return requestAttempt{
		role:      r.role.Name,
		requested: requested,
		upstream:  upstreamModel,
		model:     statsModelID(r, requested, upstreamModel),
		key:       strings.TrimSpace(cred.Name),
		session:   session,
		stream:    stream,
	}
}

// recordAttempt appends one row to the request log. It is best effort: a log
// failure is surfaced on the dashboard and never fails the request.
func (r *Runtime) recordAttempt(attempt requestAttempt, started time.Time, headers http.Header, usage reqlog.Usage, status int, errorText string, ttft, generation time.Duration, finish string, incomplete, canceled bool) {
	store := r.reqLogStore()
	if store == nil {
		return
	}
	if attempt.role == "" {
		attempt.role = r.role.Name
	}
	store.Append(reqlog.Entry{
		At:            started,
		Role:          string(attempt.role),
		Model:         attempt.model,
		Requested:     attempt.requested,
		UpstreamModel: attempt.upstream,
		Key:           attempt.key,
		Stream:        attempt.stream,
		Status:        status,
		Error:         errorText,
		LogID:         headerValue(headers, "x-opencode-log-id"),
		Session:       shortHash(attempt.session),
		TTFTms:        milliseconds(ttft),
		Generationms:  milliseconds(generation),
		Totalms:       milliseconds(time.Since(started)),
		Input:         usage.Input,
		Output:        usage.Output,
		Reasoning:     usage.Reasoning,
		CacheRead:     usage.CacheRead,
		CacheCreation: usage.CacheCreation,
		TotalTokens:   usage.Total,
		TokensSeen:    usage.Seen,
		Repairs:       attempt.repairs,
		Finish:        finish,
		Incomplete:    incomplete,
		Canceled:      canceled,
	})
}

// inboundUserAgent returns the client's User-Agent so the plugin can pass it to
// the upstream unchanged. The plugin never invents one; CPA's passthrough is
// the only source.
func inboundUserAgent(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get("User-Agent"))
}

// headerValue reads one response header with a nil-safe helper.
func headerValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(name))
}

// shortHash keeps a stable per-conversation marker without storing the raw
// x-opencode-session value.
func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:4])
}

// milliseconds renders a duration as fractional milliseconds.
func milliseconds(duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return float64(duration.Microseconds()) / 1000
}
