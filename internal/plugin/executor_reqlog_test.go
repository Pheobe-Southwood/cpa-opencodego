package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/upstream"
)

func TestPumpStreamRecordsRequestLog(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	stubHostCall(t, func(string, []byte) ([]byte, error) { return abi.OK(map[string]any{}) })

	sse := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
		"data: [DONE]\n\n"
	headers := http.Header{"X-Opencode-Log-Id": []string{"log-9"}}
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Headers:    headers,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, "stream-1", streamTiming{model: "deepseek-v4.1-flash", key: "main", started: time.Now().Add(-time.Second)})

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Model != "deepseek-v4.1-flash" || row.Key != "main" || !row.Stream || row.Status != 200 {
		t.Errorf("row = %+v", row)
	}
	if row.Input != 7 || row.Output != 3 || row.TotalTokens != 10 || row.Reasoning != 2 || !row.TokensSeen {
		t.Errorf("tokens = %+v", row)
	}
	if row.TTFTms <= 0 {
		t.Errorf("ttft = %v, want a measured value", row.TTFTms)
	}
	if row.LogID != "log-9" {
		t.Errorf("log id = %q", row.LogID)
	}
	if row.Failed() {
		t.Errorf("a 200 stream must not be failed: %+v", row)
	}
}

func TestPumpStreamRecordsFailedStream(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	stubHostCall(t, func(string, []byte) ([]byte, error) { return abi.OK(map[string]any{}) })

	sse := "data: {\"error\":{\"type\":\"server_error\",\"message\":\"boom\"}}\n\n"
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, "stream-2", streamTiming{model: "glm-5.3", key: "main", started: time.Now()})

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].Failed() {
		t.Errorf("an error frame must mark the row failed: %+v", rows[0])
	}
	if rows[0].TTFTms != 0 {
		t.Errorf("a failed stream must not contribute TTFT, got %v", rows[0].TTFTms)
	}
}

func TestHandleExecuteRecordsNonStreamRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Opencode-Log-Id", "log-http-1")
		_, _ = writer.Write([]byte(`{"id":"x","usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`))
	}))
	defer server.Close()

	runtime := New(roles.ByName("chat"))
	runtime.sharedPath = t.TempDir() + "/shared.json"
	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutLegacy
	settings.Keys = []config.Key{{Name: "main", Key: "test-key"}}
	runtime.applySettings(settings)
	t.Cleanup(runtime.Shutdown)

	request, errMarshal := json.Marshal(pluginapi.ExecutorRequest{
		Model:   "deepseek-v4.1-flash",
		Format:  "openai",
		Stream:  false,
		Payload: []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	envelopeResult(t, runtime.handleExecute(request))

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Status != 200 || row.Stream || row.Model != "deepseek-v4.1-flash" {
		t.Errorf("row = %+v", row)
	}
	if row.Input != 11 || row.Output != 5 || row.TotalTokens != 16 {
		t.Errorf("tokens = %+v", row)
	}
	if row.TTFTms != 0 {
		t.Errorf("a non-stream request has no TTFT, got %v", row.TTFTms)
	}
	if row.LogID != "log-http-1" {
		t.Errorf("log id = %q", row.LogID)
	}
	if row.Key != "main" {
		t.Errorf("key = %q", row.Key)
	}
}

func TestPumpStreamDetectsIncompleteStream(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	stubHostCall(t, func(string, []byte) ([]byte, error) { return abi.OK(map[string]any{}) })

	// Case 1: Stream closes after emitting content, but WITHOUT any finish_reason
	sseTruncated := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sseTruncated)),
	}, "stream-trunc", streamTiming{model: "deepseek-v4.1-flash", key: "main", started: time.Now().Add(-time.Second)})

	// Case 2: Normal stream ending with finish_reason: stop
	sseNormal := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sseNormal)),
	}, "stream-normal", streamTiming{model: "deepseek-v4.1-flash", key: "main", started: time.Now().Add(-time.Second)})

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	truncRow := rows[0]
	if !truncRow.Incomplete {
		t.Errorf("truncated stream must be marked incomplete: %+v", truncRow)
	}
	if truncRow.Finish != "eof" {
		t.Errorf("truncated stream finish = %q, want eof", truncRow.Finish)
	}

	normalRow := rows[1]
	if normalRow.Incomplete {
		t.Errorf("normal stream must not be marked incomplete: %+v", normalRow)
	}
	if normalRow.Finish != "stop" {
		t.Errorf("normal stream finish = %q, want stop", normalRow.Finish)
	}
}

// CPA's AuthManager selects an account and passes it as StorageJSON; the plugin
// must use that key even when another configured key has a higher priority.
func TestHandleExecuteUsesHostCredential(t *testing.T) {
	var seen atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen.Store(request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"x","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	runtime := New(roles.ByName("chat"))
	runtime.sharedPath = t.TempDir() + "/shared.json"
	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutLegacy
	settings.Keys = []config.Key{
		{Name: "main", Key: "sk-main", Priority: 10},
		{Name: "backup", Key: "sk-backup", Priority: 1},
	}
	runtime.applySettings(settings)
	t.Cleanup(runtime.Shutdown)

	request, errMarshal := json.Marshal(pluginapi.ExecutorRequest{
		Model:       "deepseek-v4.1-flash",
		Format:      "openai",
		Stream:      false,
		Payload:     []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
		AuthID:      "opencode-go-backup.json",
		StorageJSON: []byte(`{"type":"opencode-go","name":"backup","key_name":"backup","api_key":"sk-backup"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	envelopeResult(t, runtime.handleExecute(request))

	if got, _ := seen.Load().(string); got != "Bearer sk-backup" {
		t.Fatalf("upstream Authorization = %q, want the host-selected backup key", got)
	}
	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 || rows[0].Key != "backup" {
		t.Fatalf("request log = %+v, want the host-selected key", rows)
	}
}

// With more than one configured key and no host selection, the plugin must
// refuse instead of silently using the first key (the v1.10-v2.0.2 bug).
func TestHandleExecuteRefusesToPickAmongKeys(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runtime := New(roles.ByName("chat"))
	runtime.sharedPath = t.TempDir() + "/shared.json"
	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutLegacy
	settings.Keys = []config.Key{
		{Name: "main", Key: "sk-main", Priority: 10},
		{Name: "backup", Key: "sk-backup", Priority: 1},
	}
	runtime.applySettings(settings)
	t.Cleanup(runtime.Shutdown)

	request, errMarshal := json.Marshal(pluginapi.ExecutorRequest{
		Model:   "deepseek-v4.1-flash",
		Format:  "openai",
		Stream:  false,
		Payload: []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	var envelope abi.Envelope
	if errUnmarshal := json.Unmarshal(runtime.handleExecute(request), &envelope); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if envelope.OK || envelope.Error == nil {
		t.Fatalf("multi-key request without a host credential must fail: %+v", envelope)
	}
	if envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("http status = %d, want 503", envelope.Error.HTTPStatus)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream was called %d times without a selected key", calls.Load())
	}
	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 || rows[0].Key != "" || rows[0].Status != http.StatusServiceUnavailable {
		t.Fatalf("request log = %+v, want one failed row with no key", rows)
	}
}

// A downstream cancel must not be recorded as an upstream failure. The host
// binds the stream bridge to the client request context: when the client
// disconnects (or the plugin instance is unloaded) the stream is removed and
// the next emit fails with the host's closed-stream error. The upstream had
// already produced content and did not fail.
func TestPumpStreamRecordsDownstreamCancel(t *testing.T) {
	cases := []struct {
		name    string
		emitErr error
	}{
		{"stream bridge closed", errors.New("stream 42 is not open")},
		{"plugin instance unloaded", errors.New("host plugin callback instance is closed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := newStatsTestRuntime(t, roles.Chat)
			var closeNote atomic.Value
			stubHostCall(t, func(method string, payload []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostStreamEmit:
					return nil, tc.emitErr
				case pluginabi.MethodHostStreamClose:
					var request streamCloseRequest
					_ = json.Unmarshal(payload, &request)
					closeNote.Store(request.Error)
					return abi.OK(map[string]any{})
				default:
					return abi.OK(map[string]any{})
				}
			})

			sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
			runtime.pumpStream(&upstream.Stream{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, "stream-cancel", streamTiming{model: "deepseek-v4.1-flash", key: "main", started: time.Now().Add(-time.Second)})

			rows := runtime.reqLogStore().Snapshot()
			if len(rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(rows))
			}
			row := rows[0]
			if !row.Canceled {
				t.Errorf("a closed host stream must be recorded as canceled: %+v", row)
			}
			if row.Failed() {
				t.Errorf("a downstream cancel must not be a failure: %+v", row)
			}
			if row.Finish != "canceled" {
				t.Errorf("finish = %q, want canceled", row.Finish)
			}
			if row.Error != "" {
				t.Errorf("a canceled row must not keep the host callback message as an error: %q", row.Error)
			}
			if note, _ := closeNote.Load().(string); note != "" {
				t.Errorf("close note = %q, want an empty close so the host does not write to a dead stream", note)
			}
			if _, exists := runtime.statsStore().Snapshot(time.Time{}).Models["deepseek-v4.1-flash"]; exists {
				t.Error("a canceled stream must not produce a TTFT timing sample")
			}
		})
	}
}

// The classification is deliberately narrow: a host callback failure that is
// not a closed stream (e.g. a broken plugin/host envelope) is still a failure.
func TestPumpStreamKeepsNonCancelHostFailure(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	stubHostCall(t, func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostStreamEmit {
			return nil, errors.New("decode envelope: unexpected end of JSON input")
		}
		return abi.OK(map[string]any{})
	})

	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, "stream-broken", streamTiming{model: "glm-5.3", key: "main", started: time.Now()})

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Canceled {
		t.Errorf("a non-cancel host failure must not be marked canceled: %+v", row)
	}
	if !row.Failed() || row.Finish != "error" || row.Error == "" {
		t.Errorf("a non-cancel host failure must stay failed: %+v", row)
	}
}

func TestDownstreamClosedMessages(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"host callback host.stream.emit: stream 609 is not open", true},
		{"host callback host.stream.emit: host plugin callback instance is closed", true},
		{"host callback host.stream.emit: context canceled", true},
		{"host callback host.stream.emit: client closed request", true},
		{"host callback host.stream.emit: decode envelope: boom", false},
		{"upstream 502 bad gateway", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := downstreamClosedMessage(tc.message); got != tc.want {
			t.Errorf("downstreamClosedMessage(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
	if downstreamClosed(nil) {
		t.Error("a nil error is not a cancellation")
	}
	if !downstreamClosed(errors.New("stream 1 is not open")) {
		t.Error("wrapped host stream errors must be recognized")
	}
}

// An upstream error frame is authoritative even when the following emit fails
// because the client disconnected: the row must stay a failure.
func TestPumpStreamUpstreamErrorWinsOverCancel(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	stubHostCall(t, func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostStreamEmit && strings.Contains(string(payload), "server_error") {
			return nil, errors.New("stream 9 is not open")
		}
		return abi.OK(map[string]any{})
	})

	sse := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"error\":{\"type\":\"server_error\",\"message\":\"boom\"}}\n\n"
	runtime.pumpStream(&upstream.Stream{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, "stream-upstream-error", streamTiming{model: "glm-5.3", key: "main", started: time.Now()})

	rows := runtime.reqLogStore().Snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Canceled || !row.Failed() {
		t.Errorf("an upstream error frame must stay a failure even if the client leaves: %+v", row)
	}
	if row.Finish != "error" || row.Error == "" {
		t.Errorf("finish=%q error=%q, want error with a message", row.Finish, row.Error)
	}
}
