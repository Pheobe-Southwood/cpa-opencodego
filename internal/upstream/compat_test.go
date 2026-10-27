package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("body is not JSON: %v (%s)", errUnmarshal, raw)
	}
	return decoded
}

func TestNormalizeDeveloperRolesChat(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}],"stream":true}`)
	out := normalizeDeveloperRoles(roles.ByName("chat"), body, true)
	decoded := decodeBody(t, out)
	messages := decoded["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("developer role = %v, want system", first["role"])
	}
	if first["content"] != "sys" || decoded["model"] != "deepseek-v4.1-flash" || decoded["stream"] != true {
		t.Errorf("unrelated fields changed: %s", out)
	}
	second := messages[1].(map[string]any)
	if second["role"] != "user" {
		t.Errorf("a non-developer role must be preserved: %v", second["role"])
	}
}

func TestNormalizeDeveloperRolesResponsesInput(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sys"}]},{"role":"user","content":"hi"}]}`)
	out := normalizeDeveloperRoles(roles.ByName("responses"), body, true)
	decoded := decodeBody(t, out)
	items := decoded["input"].([]any)
	first := items[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("input developer role = %v, want system", first["role"])
	}
	if first["type"] != "message" {
		t.Errorf("the item type must be preserved: %v", first["type"])
	}
}

func TestNormalizeDeveloperRolesNoops(t *testing.T) {
	plain := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if got := normalizeDeveloperRoles(roles.ByName("chat"), plain, true); string(got) != string(plain) {
		t.Errorf("a body without developer must not be re-encoded: %s", got)
	}
	keep := []byte(`{"messages":[{"role":"developer","content":"sys"}]}`)
	if got := normalizeDeveloperRoles(roles.ByName("chat"), keep, false); string(got) != string(keep) {
		t.Errorf("developer-role=keep must pass through: %s", got)
	}
	// The Anthropic and GenAI bodies have no messages[].role concept.
	if got := normalizeDeveloperRoles(roles.ByName("messages"), keep, true); string(got) != string(keep) {
		t.Errorf("the messages family must not be rewritten: %s", got)
	}
	broken := []byte(`{"messages":[{"role":"developer"`)
	if got := normalizeDeveloperRoles(roles.ByName("chat"), broken, true); string(got) != string(broken) {
		t.Errorf("invalid JSON must pass through unchanged")
	}
}

type capturedRequest struct {
	Path    string
	Headers http.Header
	Body    []byte
}

// The plugin must forward the client's User-Agent verbatim and never invent
// one; x-opencode-session is the only header the plugin owns.
func TestUserAgentPassthroughAndEndpoint(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []capturedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		seen = append(seen, capturedRequest{Path: request.URL.Path, Headers: request.Header.Clone(), Body: raw})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.SessionMode = "fixed"
	settings.Session = "session-1"
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	if _, errExecute := client.Execute(t.Context(), Call{
		Role:      roles.ByName("chat"),
		Model:     "deepseek-v4.1-flash",
		Body:      []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"}]}`),
		Session:   "session-1",
		UserAgent: "my-coding-agent/1.0",
	}); errExecute != nil {
		t.Fatalf("Execute: %v", errExecute)
	}
	if _, errExecute := client.Execute(t.Context(), Call{
		Role:    roles.ByName("responses"),
		Model:   "gpt-6-luna",
		Body:    []byte(`{"model":"gpt-6-luna","input":"hi"}`),
		Session: "session-1",
	}); errExecute != nil {
		t.Fatalf("Execute: %v", errExecute)
	}

	mu.Lock()
	initialSeen := append([]capturedRequest(nil), seen...)
	mu.Unlock()

	if len(initialSeen) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(initialSeen))
	}
	if initialSeen[0].Path != "/openai/v1/chat/completions" {
		t.Errorf("chat path = %q", initialSeen[0].Path)
	}
	if initialSeen[1].Path != "/openai/v1/responses" {
		t.Errorf("responses path = %q", initialSeen[1].Path)
	}
	if got := initialSeen[0].Headers.Get("User-Agent"); got != "my-coding-agent/1.0" {
		t.Errorf("User-Agent = %q, want the inbound value", got)
	}
	if got := initialSeen[1].Headers.Get("User-Agent"); !strings.HasPrefix(got, "Go-http-client/") {
		t.Errorf("without an inbound UA the plugin must not synthesise one, got %q", got)
	}
	if got := initialSeen[0].Headers.Get("x-opencode-session"); got != "session-1" {
		t.Errorf("x-opencode-session = %q", got)
	}
	// Under default retry-only mode, developer role is preserved on the wire on attempt 1.
	if !strings.Contains(string(initialSeen[0].Body), `"role":"developer"`) || strings.Contains(string(initialSeen[0].Body), `"role":"system"`) {
		t.Errorf("developer role must be preserved on the wire in retry-only default: %s", initialSeen[0].Body)
	}
	// With developer-role: rewrite, the role is rewritten proactively.
	settingsRewrite := settings
	settingsRewrite.DeveloperRole = config.DeveloperRoleRewrite
	clientRewrite := New(settingsRewrite)
	if _, err := clientRewrite.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"}]}`),
	}); err != nil {
		t.Fatalf("Execute with rewrite failed: %v", err)
	}

	mu.Lock()
	last := seen[len(seen)-1]
	mu.Unlock()
	if !strings.Contains(string(last.Body), `"role":"system"`) || strings.Contains(string(last.Body), `"role":"developer"`) {
		t.Errorf("developer role must be rewritten with mode=rewrite: %s", last.Body)
	}
}

func assistantReasoning(t *testing.T, raw []byte, index int) (string, bool) {
	t.Helper()
	var decoded struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("body is not JSON: %v (%s)", errUnmarshal, raw)
	}
	if index >= len(decoded.Messages) {
		t.Fatalf("messages index %d out of range", index)
	}
	value, ok := decoded.Messages[index]["reasoning_content"]
	if !ok {
		return "", false
	}
	var text string
	if errUnmarshal := json.Unmarshal(value, &text); errUnmarshal != nil {
		t.Fatalf("reasoning_content is not a string: %s", value)
	}
	return text, true
}

func TestEnsureReasoningContentFillsAssistantHistory(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"max","messages":[` +
		`{"role":"system","content":"sys"},` +
		`{"role":"user","content":"read a.txt"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"hello"},` +
		`{"role":"assistant","content":"done"}]}`)
	out := ensureReasoningContent(roles.ByName("chat"), body, config.ReasoningReplayAuto)
	for _, index := range []int{2, 4} {
		if text, ok := assistantReasoning(t, out, index); !ok || text != config.DefaultReasoningPlaceholder {
			t.Errorf("messages[%d].reasoning_content = %q (present=%v), want the placeholder", index, text, ok)
		}
	}
	if _, ok := assistantReasoning(t, out, 0); ok {
		t.Errorf("the system message must not gain reasoning_content")
	}
	decoded := decodeBody(t, out)
	if decoded["reasoning_effort"] != "max" || decoded["model"] != "deepseek-v4.1-flash" {
		t.Errorf("top-level fields changed: %s", out)
	}
}

func TestEnsureReasoningContentKeepsExistingAndReplacesEmpty(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"a","reasoning_content":"real thought"},` +
		`{"role":"assistant","content":"b","reasoning_content":""}]}`)
	out := ensureReasoningContent(roles.ByName("chat"), body, config.ReasoningReplayAuto)
	if text, _ := assistantReasoning(t, out, 1); text != "real thought" {
		t.Errorf("existing reasoning_content was rewritten: %q", text)
	}
	if text, _ := assistantReasoning(t, out, 2); text != config.DefaultReasoningPlaceholder {
		t.Errorf("empty reasoning_content must be filled, got %q", text)
	}
}

func TestEnsureReasoningContentRespectsModeAuto(t *testing.T) {
	plain := []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}]}`)
	if got := ensureReasoningContent(roles.ByName("chat"), plain, config.ReasoningReplayAuto); string(got) != string(plain) {
		t.Errorf("a request without thinking indicators must pass through: %s", got)
	}
	disabled := []byte(`{"model":"m","reasoning_effort":"none","messages":[{"role":"assistant","content":"ok"}]}`)
	if got := ensureReasoningContent(roles.ByName("chat"), disabled, config.ReasoningReplayAuto); string(got) != string(disabled) {
		t.Errorf("reasoning_effort=none must not be filled: %s", got)
	}
	if got := ensureReasoningContent(roles.ByName("chat"), plain, config.ReasoningReplayOff); string(got) != string(plain) {
		t.Errorf("reasoning-replay=off must pass through: %s", got)
	}
	always := ensureReasoningContent(roles.ByName("chat"), plain, config.ReasoningReplayAlways)
	if text, ok := assistantReasoning(t, always, 1); !ok || text != config.DefaultReasoningPlaceholder {
		t.Errorf("reasoning-replay=always must fill every assistant message: %s", always)
	}
	// The Anthropic and GenAI bodies use thinking blocks, not reasoning_content.
	if got := ensureReasoningContent(roles.ByName("messages"), plain, config.ReasoningReplayAlways); string(got) != string(plain) {
		t.Errorf("the messages family must not be rewritten: %s", got)
	}
	broken := []byte(`{"messages":[{"role":"assistant"`)
	if got := ensureReasoningContent(roles.ByName("chat"), broken, config.ReasoningReplayAlways); string(got) != string(broken) {
		t.Errorf("invalid JSON must pass through unchanged")
	}
}

func TestEnsureReasoningContentDetectsThinkingObjects(t *testing.T) {
	body := []byte(`{"model":"m","thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":"ok"}]}`)
	out := ensureReasoningContent(roles.ByName("chat"), body, config.ReasoningReplayAuto)
	if _, ok := assistantReasoning(t, out, 0); !ok {
		t.Errorf("thinking.type=enabled must enable the fill: %s", out)
	}
	off := []byte(`{"model":"m","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":"ok"}]}`)
	if got := ensureReasoningContent(roles.ByName("chat"), off, config.ReasoningReplayAuto); string(got) != string(off) {
		t.Errorf("thinking.type=disabled must not be filled: %s", got)
	}
}

func TestExecuteRetriesReasoningReplayRejection(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"invalid_request_error: Upstream request failed: [invalid_request_error] The ` + "`reasoning_content`" + ` in the thinking mode must be passed back to the API."}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"id":"ok","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)
	result, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"max","messages":[{"role":"assistant","content":"ok"}]}`),
	})
	if errExecute != nil {
		t.Fatalf("Execute must retry the reasoning-passback 400: %v", errExecute)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2", calls)
	}
}

func TestExecuteDoesNotRetryOtherBadRequests(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid_request_error: unsupported parameter"}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)
	if _, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "m",
		Body:  []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	}); errExecute == nil {
		t.Fatalf("a non-reasoning 400 must be returned as an error")
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry)", calls)
	}
}

func TestIsDeveloperRoleRejection(t *testing.T) {
	cases := []struct {
		body []byte
		want bool
	}{
		{[]byte(`{"error":{"message":"chat message role ` + "`developer`" + ` is not supported by the router gRPC frontend"}}`), true},
		{[]byte(`{"error":{"message":"role developer is not supported"}}`), true},
		// DeepSeek gateway serde rejection, delivered as HTTP 422.
		{[]byte(`{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant ` + "`developer`" + `, expected one of ` + "`system`" + `, ` + "`user`" + `"}}`), true},
		// Qwen gateway wording.
		{[]byte(`{"error":{"message":"invalid_parameter_error: developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"}}`), true},
		{[]byte(`{"error":{"message":"The reasoning_content in the thinking mode must be passed back"}}`), false},
		{[]byte(`{"error":{"message":"developer role is great"}}`), false},
		{[]byte(`{"error":{"message":"model not supported"}}`), false},
		{[]byte(`{"error":{"message":"Endpoint is unavailable."}}`), false},
		{[]byte(`{"error":{"message":"unknown variant ` + "`image_url`" + `"}}`), false},
	}
	for _, tc := range cases {
		if got := isDeveloperRoleRejection(tc.body); got != tc.want {
			t.Errorf("isDeveloperRoleRejection(%s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestIsTransientRejection(t *testing.T) {
	cases := []struct {
		body []byte
		want bool
	}{
		{[]byte(`{"model":"deepseek-v4.1-flash"}`), true},
		{[]byte(`{"error":{"message":"invalid_request_error: This response_format type is unavailable now"}}`), true},
		{[]byte(`{"error":{"message":"invalid_request_error: unsupported parameter"}}`), false},
		{[]byte(`{"error":{"message":"model not found"}}`), false},
		{[]byte(`not json`), false},
	}
	for _, tc := range cases {
		if got := isTransientRejection(tc.body); got != tc.want {
			t.Errorf("isTransientRejection(%s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestExecuteRetriesTransient400Once(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "opaque_model_body", body: `{"model":"deepseek-v4.1-flash"}`},
		{name: "response_format_unavailable", body: `{"error":{"message":"invalid_request_error: This response_format type is unavailable now"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen []capturedRequest
			)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				raw, _ := io.ReadAll(request.Body)
				mu.Lock()
				seen = append(seen, capturedRequest{Path: request.URL.Path, Body: raw})
				calls := len(seen)
				mu.Unlock()
				if calls == 1 {
					writer.WriteHeader(http.StatusBadRequest)
					_, _ = writer.Write([]byte(tc.body))
					return
				}
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer server.Close()

			settings := config.Default()
			settings.BaseURL = server.URL
			settings.EndpointLayout = config.LayoutFamily
			settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
			client := New(settings)
			body := []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}}}`)

			result, errExecute := client.Execute(t.Context(), Call{
				Role:  roles.ByName("chat"),
				Model: "deepseek-v4.1-flash",
				Body:  body,
			})
			if errExecute != nil {
				t.Fatalf("Execute failed: %v", errExecute)
			}
			if result.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", result.StatusCode)
			}
			if len(result.Repairs) != 0 {
				t.Errorf("repairs = %v, want none for a transient retry", result.Repairs)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 2 {
				t.Fatalf("upstream calls = %d, want 2", len(seen))
			}
			if !bytes.Equal(seen[0].Body, seen[1].Body) {
				t.Errorf("transient retry must not rewrite the body: %s vs %s", seen[0].Body, seen[1].Body)
			}
		})
	}
}

func TestExecuteRetriesEndpointUnavailable503Once(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	result, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4-pro",
		Body:  []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute != nil {
		t.Fatalf("Execute must retry the endpoint-unavailable 503: %v", errExecute)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
}

func TestExecuteEndpointUnavailableRetryIsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	_, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4-pro",
		Body:  []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute == nil {
		t.Fatal("a persistent 503 must surface as an error")
	}
	if got := abi.Status(errExecute); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (one retry only)", got)
	}
}

func TestExecuteDoesNotRetryOther503(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"type":"server_error","message":"upstream overloaded"}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	_, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4-pro",
		Body:  []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute == nil {
		t.Fatal("a generic 503 must surface as an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry for an unclassified 503)", got)
	}
}

func TestExecuteDeveloperRoleRetryOnlyLadder(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []capturedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		seen = append(seen, capturedRequest{Path: request.URL.Path, Body: raw})
		calls := len(seen)
		mu.Unlock()

		if calls == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"chat message role ` + "`developer`" + ` is not supported by the router gRPC frontend"}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	result, err := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"}]}`),
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if len(result.Repairs) != 1 || result.Repairs[0] != "developer-role" {
		t.Errorf("repairs = %v, want [developer-role]", result.Repairs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(seen))
	}
	if !strings.Contains(string(seen[0].Body), `"role":"developer"`) {
		t.Errorf("attempt 1 must preserve developer role: %s", seen[0].Body)
	}
	if !strings.Contains(string(seen[1].Body), `"role":"system"`) || strings.Contains(string(seen[1].Body), `"role":"developer"`) {
		t.Errorf("attempt 2 must rewrite developer to system: %s", seen[1].Body)
	}
}

func TestExecuteDeveloperRoleRepairsVariantRejections(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "serde_422",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[0].role: unknown variant ` + "`developer`" + `, expected one of ` + "`system`" + `, ` + "`user`" + `"}}`,
		},
		{
			name:   "qwen_400",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"invalid_parameter_error: developer is not one of ['system', 'assistant', 'user', 'tool', 'function']"}}`,
		},
		{
			name:   "opaque_422",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":{"message":"server_error: Upstream request failed: Endpoint is unavailable."}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen []capturedRequest
			)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				raw, _ := io.ReadAll(request.Body)
				mu.Lock()
				seen = append(seen, capturedRequest{Path: request.URL.Path, Body: raw})
				calls := len(seen)
				mu.Unlock()
				if calls == 1 {
					writer.WriteHeader(tc.status)
					_, _ = writer.Write([]byte(tc.body))
					return
				}
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer server.Close()

			settings := config.Default()
			settings.BaseURL = server.URL
			settings.EndpointLayout = config.LayoutFamily
			settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
			client := New(settings)

			result, errExecute := client.Execute(t.Context(), Call{
				Role:  roles.ByName("chat"),
				Model: "deepseek-v4.1-flash",
				Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"}]}`),
			})
			if errExecute != nil {
				t.Fatalf("Execute failed: %v", errExecute)
			}
			if result.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", result.StatusCode)
			}
			if len(result.Repairs) != 1 || result.Repairs[0] != "developer-role" {
				t.Errorf("repairs = %v, want [developer-role]", result.Repairs)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 2 {
				t.Fatalf("upstream calls = %d, want 2", len(seen))
			}
			if !strings.Contains(string(seen[1].Body), `"role":"system"`) || strings.Contains(string(seen[1].Body), `"role":"developer"`) {
				t.Errorf("attempt 2 must rewrite developer to system: %s", seen[1].Body)
			}
		})
	}
}

func TestExecuteBothDeveloperAndReasoningLadder(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []capturedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		seen = append(seen, capturedRequest{Path: request.URL.Path, Body: raw})
		calls := len(seen)
		mu.Unlock()

		if calls == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"chat message role ` + "`developer`" + ` is not supported by the router gRPC frontend"}}`))
			return
		}
		if calls == 2 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	result, err := client.Execute(t.Context(), Call{
		Role:    roles.ByName("chat"),
		Model:   "deepseek-v4.1-flash",
		Session: "sess-ladder",
		Body:    []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"sys"},{"role":"assistant","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if len(result.Repairs) != 2 || result.Repairs[0] != "developer-role" || result.Repairs[1] != "reasoning-replay" {
		t.Errorf("repairs = %v, want [developer-role reasoning-replay]", result.Repairs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("upstream calls = %d, want 3", len(seen))
	}
	// Attempt 1: has developer, no placeholder
	if !strings.Contains(string(seen[0].Body), `"role":"developer"`) || strings.Contains(string(seen[0].Body), "reasoning_content") {
		t.Errorf("attempt 1: %s", seen[0].Body)
	}
	// Attempt 2: system, no placeholder
	if !strings.Contains(string(seen[1].Body), `"role":"system"`) || strings.Contains(string(seen[1].Body), "reasoning_content") {
		t.Errorf("attempt 2: %s", seen[1].Body)
	}
	// Attempt 3: system AND placeholder
	if !strings.Contains(string(seen[2].Body), `"role":"system"`) || !strings.Contains(string(seen[2].Body), "reasoning_content") {
		t.Errorf("attempt 3: %s", seen[2].Body)
	}
}

func TestExecuteSessionStickyReasoning(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []capturedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		seen = append(seen, capturedRequest{Path: request.URL.Path, Body: raw})
		calls := len(seen)
		mu.Unlock()

		if calls == 1 {
			// First call ever gets rejected for missing reasoning
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	// Turn 1 on sess-sticky: triggers 400 rejection and repairs
	res1, err1 := client.Execute(t.Context(), Call{
		Role:    roles.ByName("chat"),
		Model:   "deepseek-v4.1-flash",
		Session: "sess-sticky",
		Body:    []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"max","messages":[{"role":"assistant","content":"turn1"}]}`),
	})
	if err1 != nil || res1.StatusCode != http.StatusOK {
		t.Fatalf("Turn 1 failed: %v", err1)
	}

	mu.Lock()
	turn1Calls := len(seen)
	mu.Unlock()
	if turn1Calls != 2 {
		t.Fatalf("turn 1 calls = %d, want 2 (initial + repair)", turn1Calls)
	}

	// Turn 2 on SAME session: should proactively pre-fill reasoning_content, so only 1 upstream call!
	res2, err2 := client.Execute(t.Context(), Call{
		Role:    roles.ByName("chat"),
		Model:   "deepseek-v4.1-flash",
		Session: "sess-sticky",
		Body:    []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"max","messages":[{"role":"assistant","content":"turn2"}]}`),
	})
	if err2 != nil || res2.StatusCode != http.StatusOK {
		t.Fatalf("Turn 2 failed: %v", err2)
	}

	mu.Lock()
	turn2Calls := len(seen) - turn1Calls
	attempt3Body := string(seen[2].Body)
	mu.Unlock()

	if turn2Calls != 1 {
		t.Fatalf("turn 2 calls = %d, want 1 (sticky pre-filled, no retry needed)", turn2Calls)
	}
	if !strings.Contains(attempt3Body, config.DefaultReasoningPlaceholder) {
		t.Errorf("turn 2 first attempt body must contain placeholder due to sticky memory: %s", attempt3Body)
	}

	// Turn 3 on a DIFFERENT session: must NOT be sticky on attempt 1
	res3, err3 := client.Execute(t.Context(), Call{
		Role:    roles.ByName("chat"),
		Model:   "deepseek-v4.1-flash",
		Session: "sess-different",
		Body:    []byte(`{"model":"deepseek-v4.1-flash","reasoning_effort":"max","messages":[{"role":"assistant","content":"turn3"}]}`),
	})
	if err3 != nil || res3.StatusCode != http.StatusOK {
		t.Fatalf("Turn 3 failed: %v", err3)
	}

	mu.Lock()
	attempt4Body := string(seen[3].Body)
	mu.Unlock()

	if strings.Contains(attempt4Body, config.DefaultReasoningPlaceholder) {
		t.Errorf("different session must not inherit sticky reasoning on attempt 1: %s", attempt4Body)
	}
}

func TestExecuteKeepAndOffDoNotRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"chat message role ` + "`developer`" + ` is not supported"}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.DeveloperRole = config.DeveloperRoleKeep
	settings.ReasoningReplay = config.ReasoningReplayOff
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	_, err := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"developer","content":"hi"}]}`),
	})
	if err == nil {
		t.Fatalf("must return error when keep mode receives 400")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry when developer-role=keep)", calls)
	}
}

func TestAdmissionRefusalRetryDelay(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		delay time.Duration
		ok    bool
	}{
		{
			name:  "hint is honoured",
			body:  `{"error":{"type":"server_error","message":"Upstream request failed: [ttft_admission_refused] the cell is at capacity; retry shortly (after 16 s)"}}`,
			delay: 16 * time.Second,
			ok:    true,
		},
		{
			name:  "hint is capped",
			body:  `{"error":{"type":"server_error","message":"Upstream request failed: [ttft_admission_refused] the cell is at capacity; retry shortly (after 500 s)"}}`,
			delay: admissionRetryMaxDelay,
			ok:    true,
		},
		{
			name:  "missing hint falls back to the short delay",
			body:  `{"error":{"type":"server_error","message":"[ttft_admission_refused] the cell is at capacity"}}`,
			delay: endpointUnavailableRetryDelay,
			ok:    true,
		},
		{
			name: "quota failure is not an admission refusal",
			body: `{"error":{"code":"GoUsageLimitError","message":"Go usage limit exceeded"}}`,
		},
		{
			name: "opaque body is not an admission refusal",
			body: `{"model":"deepseek-v4.1-flash"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delay, ok := admissionRefusalRetryDelay([]byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && delay != tc.delay {
				t.Errorf("delay = %s, want %s", delay, tc.delay)
			}
		})
	}
}

func TestExecuteRetriesAdmissionRefusalAfterHint(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: [ttft_admission_refused] the cell is at capacity; retry shortly (after 1 s)"}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	started := time.Now()
	result, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute != nil {
		t.Fatalf("Execute must retry the admission refusal: %v", errExecute)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Errorf("retry happened after %s, want the 1s hint to be honoured", elapsed)
	}
}

func TestExecuteAdmissionRetryIsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: [ttft_admission_refused] the cell is at capacity; retry shortly (after 0 s)"}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	_, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute == nil {
		t.Fatal("a persistent admission refusal must surface as an error")
	}
	if got := abi.Status(errExecute); got != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", got)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (one retry only)", got)
	}
}

func TestExecuteDoesNotRetryQuota429(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"code":"GoUsageLimitError","message":"Go usage limit exceeded"}}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	_, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute == nil {
		t.Fatal("a quota 429 must surface as an error")
	}
	if got := abi.Status(errExecute); got != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", got)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (quota must not be retried)", got)
	}
}

func TestExecuteRetriesOpaque429Once(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"model":"deepseek-v4.1-flash"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	settings.BaseURL = server.URL
	settings.EndpointLayout = config.LayoutFamily
	settings.Keys = []config.Key{{Name: "k1", Key: "test-key"}}
	client := New(settings)

	result, errExecute := client.Execute(t.Context(), Call{
		Role:  roles.ByName("chat"),
		Model: "deepseek-v4.1-flash",
		Body:  []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if errExecute != nil {
		t.Fatalf("Execute must retry the opaque 429: %v", errExecute)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
}
