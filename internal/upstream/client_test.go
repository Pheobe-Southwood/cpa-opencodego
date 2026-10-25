package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

func TestForceStreamFlag(t *testing.T) {
	// A non-streaming request without the field is returned untouched.
	plain := []byte(`{"model":"m","messages":[]}`)
	if got := string(forceStreamFlag(plain, false)); got != string(plain) {
		t.Errorf("unexpected rewrite: %s", got)
	}

	// Streaming always sets the field.
	var decoded map[string]any
	streamed := forceStreamFlag([]byte(`{"model":"m"}`), true)
	if errUnmarshal := json.Unmarshal(streamed, &decoded); errUnmarshal != nil {
		t.Fatalf("rewritten body is not JSON: %v", errUnmarshal)
	}
	if decoded["stream"] != true {
		t.Errorf("stream flag not set: %s", streamed)
	}

	// A stale true is cleared for non-streaming requests.
	cleared := forceStreamFlag([]byte(`{"model":"m","stream":true}`), false)
	if errUnmarshal := json.Unmarshal(cleared, &decoded); errUnmarshal != nil {
		t.Fatalf("cleared body is not JSON: %v", errUnmarshal)
	}
	if decoded["stream"] != false {
		t.Errorf("stream flag not cleared: %s", cleared)
	}

	// A body that is not a JSON object passes through unchanged.
	broken := []byte(`not json`)
	if got := string(forceStreamFlag(broken, true)); got != string(broken) {
		t.Errorf("non-object body must pass through, got %s", got)
	}
}

// httpClient must key its cache by the resolved proxy so that flipping one model
// between direct and proxied cannot reuse the wrong transport.
func TestHTTPClientCacheIsKeyedByProxy(t *testing.T) {
	settings, errParse := config.Parse([]byte(`
proxy-url: "http://127.0.0.1:7890"
default-proxy: proxy
models:
  "gpt-6-luna":
    proxy: direct
`))
	if errParse != nil {
		t.Fatalf("Parse: %v", errParse)
	}
	client := New(settings)

	proxied, errProxied := client.httpClient(settings, Call{Role: roles.ByName("responses"), Model: "grok-4.7"})
	if errProxied != nil {
		t.Fatalf("proxied client: %v", errProxied)
	}
	direct, errDirect := client.httpClient(settings, Call{Role: roles.ByName("responses"), Model: "gpt-6-luna"})
	if errDirect != nil {
		t.Fatalf("direct client: %v", errDirect)
	}
	if proxied == direct {
		t.Fatal("a direct model must not share the proxied transport")
	}
	if direct.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("the direct transport must not carry a proxy function")
	}
	if proxied.Transport.(*http.Transport).Proxy == nil {
		t.Fatal("the proxied transport must carry a proxy function")
	}
}

func TestSummarizeUpstreamError(t *testing.T) {
	cases := map[string]string{
		`{"error":{"type":"AuthError","message":"Unauthorized"}}`: "AuthError: Unauthorized",
		`{"error":{"message":"boom"}}`:                            "boom",
		`{"message":"plain"}`:                                     "plain",
		`{"type":"error","error":{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol."}}`: "ModelProtocolUnsupported: Model does not support this protocol.",
	}
	for body, want := range cases {
		if got := summarizeUpstreamError([]byte(body)); got != want {
			t.Errorf("summarizeUpstreamError(%s) = %q, want %q", body, got, want)
		}
	}
}

// A 403 that the gateway raises for the request itself must not be reported as
// a credential fault: CLIProxyAPI cools a key for 30 minutes on any 403, which
// would take the whole model offline after one refused prompt.
func TestUpstreamErrorKeepsRequestScoped403OutOfCredentialCooldown(t *testing.T) {
	refusal := []byte(`{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`)
	errRefusal := upstreamError(http.StatusForbidden, refusal, "keyg")
	if got := abi.Status(errRefusal); got != http.StatusBadRequest {
		t.Errorf("request-scoped refusal status = %d, want 400", got)
	}
	if strings.Contains(errRefusal.Error(), "[keyg]") {
		t.Errorf("a request-scoped refusal must not be attributed to a key: %v", errRefusal)
	}
	if !strings.Contains(errRefusal.Error(), "Endpoint is unavailable") {
		t.Errorf("the upstream message must be preserved: %v", errRefusal)
	}

	// A genuine credential/permission 403 keeps its status and failover hint.
	authFailure := []byte(`{"error":{"type":"AuthError","message":"Forbidden"}}`)
	errAuth := upstreamError(http.StatusForbidden, authFailure, "keyg")
	if got := abi.Status(errAuth); got != http.StatusForbidden {
		t.Errorf("credential 403 status = %d, want 403", got)
	}
	if !strings.Contains(errAuth.Error(), "[keyg]") {
		t.Errorf("a credential 403 should name the key: %v", errAuth)
	}

	// A server error on another status keeps its status as well.
	errUpstream := upstreamError(http.StatusBadGateway, []byte(`{"error":{"type":"server_error","message":"bad gateway"}}`), "keyg")
	if got := abi.Status(errUpstream); got != http.StatusBadGateway {
		t.Errorf("502 status = %d, want 502", got)
	}
}

func TestRetryableStatus(t *testing.T) {
	retryable := []int{401, 403, 408, 429, 500, 502, 503, 504}
	for _, status := range retryable {
		if !retryableStatus(status) {
			t.Errorf("status %d should trigger failover", status)
		}
	}
	for _, status := range []int{400, 404, 422} {
		if retryableStatus(status) {
			t.Errorf("status %d must not trigger failover", status)
		}
	}
}

// An alias must be rewritten in the request body, and only then.
func TestRewriteModelField(t *testing.T) {
	body := []byte(`{"model":"grok","stream":true,"input":"hi"}`)

	rewritten := rewriteModelField(body, "grok", "grok-4.7")
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(rewritten, &decoded); errUnmarshal != nil {
		t.Fatalf("rewritten body is not JSON: %v", errUnmarshal)
	}
	if decoded["model"] != "grok-4.7" {
		t.Errorf("model = %v, want grok-4.7", decoded["model"])
	}
	if decoded["input"] != "hi" || decoded["stream"] != true {
		t.Errorf("unrelated fields changed: %s", rewritten)
	}

	// Identical ids are a no-op and must not re-encode the body.
	if got := rewriteModelField(body, "grok-4.7", "grok-4.7"); string(got) != string(body) {
		t.Errorf("identical ids should be untouched, got %s", got)
	}
	// A body that does not name the requested model is left alone.
	if got := rewriteModelField(body, "something-else", "grok-4.7"); string(got) != string(body) {
		t.Errorf("a mismatched body should be untouched, got %s", got)
	}
	// A body without a model field (GenAI style) is left alone.
	geminiBody := []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	if got := rewriteModelField(geminiBody, "grok", "grok-4.7"); string(got) != string(geminiBody) {
		t.Errorf("a body without a model field should be untouched, got %s", got)
	}
}

// The plugin must not choose among several configured keys: CPA's AuthManager
// selects one and passes it in Call.Credential. Only a single-key deployment may
// fall back to its only key.
func TestResolveCredentialRequiresHostSelectionForMultipleKeys(t *testing.T) {
	client := New(config.Default())
	settings := config.Default()

	if _, err := client.resolveCredential(Call{}, settings); err == nil || abi.Status(err) != http.StatusUnauthorized {
		t.Fatalf("no keys: err = %v, want 401", err)
	}

	settings.Keys = []config.Key{{Name: "only", Key: "sk-only"}}
	credential, err := client.resolveCredential(Call{}, settings)
	if err != nil || credential.Name != "only" || credential.Key != "sk-only" {
		t.Fatalf("single key: credential = %+v, err = %v", credential, err)
	}

	settings.Keys = []config.Key{
		{Name: "main", Key: "sk-main", Priority: 10},
		{Name: "backup", Key: "sk-backup", Priority: 1},
	}
	if _, err = client.resolveCredential(Call{}, settings); err == nil || abi.Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("multiple keys without a host auth: err = %v, want 503", err)
	}

	hostSelected := config.Key{Name: "backup", Key: "sk-backup"}
	credential, err = client.resolveCredential(Call{Credential: hostSelected}, settings)
	if err != nil || credential.Key != "sk-backup" {
		t.Fatalf("host credential: credential = %+v, err = %v", credential, err)
	}
}
