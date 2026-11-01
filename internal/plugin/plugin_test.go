package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

func rolesChat() roles.Role { return roles.ByName("chat") }

func rolesGemini() roles.Role { return roles.ByName("gemini") }

// envelopeResult unwraps a successful RPC envelope and fails the test when the
// plugin reported an error instead.
func envelopeResult(t *testing.T, raw []byte) []byte {
	t.Helper()
	var envelope abi.Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("response is not an envelope: %v (%s)", errUnmarshal, raw)
	}
	if !envelope.OK {
		t.Fatalf("plugin returned an error envelope: %s", raw)
	}
	return envelope.Result
}

func TestBareModelName(t *testing.T) {
	cases := map[string]string{
		"grok-4.7":                   "grok-4.7",
		"gpt-6-luna(high)":           "gpt-6-luna",
		"gpt-6-luna(8192)":           "gpt-6-luna",
		"(high)":                     "(high)",
		"  deepseek-v4-pro  ":        "deepseek-v4-pro",
		"gemini-4-argon-contributor": "gemini-4-argon-contributor",
	}
	for input, want := range cases {
		if got := bareModelName(input); got != want {
			t.Errorf("bareModelName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveSession(t *testing.T) {
	auto := config.Default()
	auto.Session = "fallback"
	if got := resolveSession(auto, map[string]any{"canonical_session_id": "ses_abc"}); got != "ses_abc" {
		t.Errorf("auto mode must prefer the host session, got %q", got)
	}
	if got := resolveSession(auto, map[string]any{"execution_session_id": "ses_exec"}); got != "ses_exec" {
		t.Errorf("auto mode must accept execution_session_id, got %q", got)
	}
	if got := resolveSession(auto, nil); got != "fallback" {
		t.Errorf("auto mode must fall back to the configured value, got %q", got)
	}

	fixed := config.Default()
	fixed.SessionMode = "fixed"
	fixed.Session = "$CPA-SESSION-ID"
	if got := resolveSession(fixed, map[string]any{"canonical_session_id": "ses_abc"}); got != "$CPA-SESSION-ID" {
		t.Errorf("fixed mode must ignore metadata, got %q", got)
	}
}

func TestForwardHeadersDropsHopByHop(t *testing.T) {
	in := http.Header{
		"Content-Type":     []string{"application/json"},
		"Content-Length":   []string{"123"},
		"Content-Encoding": []string{"gzip"},
		"Set-Cookie":       []string{"a=b"},
		"X-Request-Id":     []string{"abc"},
	}
	out := forwardHeaders(in)
	if _, present := out["Content-Length"]; present {
		t.Error("Content-Length must be dropped")
	}
	if _, present := out["Content-Encoding"]; present {
		t.Error("Content-Encoding must be dropped because Go already decoded the body")
	}
	if _, present := out["Set-Cookie"]; present {
		t.Error("Set-Cookie must not be forwarded")
	}
	if out.Get("Content-Type") != "application/json" || out.Get("X-Request-Id") != "abc" {
		t.Errorf("useful headers were lost: %v", out)
	}
}

// The model router must never force this provider ahead of the host's own
// model-to-provider arbitration. A native channel for the same model id is how
// the operator adds a fallback; a forced route then reaches a provider whose
// executor the host did not register, and fails with auth_not_found even though
// the keys have quota.
func TestModelRouterDeclinesHostArbitratedModels(t *testing.T) {
	for _, role := range roles.All {
		runtime := New(role)
		runtime.applySettings(config.Default())
		request, errMarshal := json.Marshal(pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-argon"})
		if errMarshal != nil {
			t.Fatalf("marshal route request: %v", errMarshal)
		}
		var envelope struct {
			OK     bool               `json:"ok"`
			Result modelRouteResponse `json:"result"`
		}
		if errUnmarshal := json.Unmarshal(runtime.handleModelRoute(request), &envelope); errUnmarshal != nil {
			t.Fatalf("decode route response: %v", errUnmarshal)
		}
		if !envelope.OK {
			t.Fatalf("role %s route returned an error", role.Name)
		}
		if envelope.Result.Handled {
			t.Fatalf("role %s claimed a model the host may arbitrate: %+v", role.Name, envelope.Result)
		}
		if envelope.Result.Target != "" || envelope.Result.TargetModel != "" {
			t.Fatalf("role %s must not target a provider when declining: %+v", role.Name, envelope.Result)
		}
	}
}

func TestRegistrationDeclaresSingleFormat(t *testing.T) {
	runtime := New(rolesChat())
	raw := runtime.registrationPayload()

	var decoded struct {
		SchemaVersion uint32 `json:"schema_version"`
		Capabilities  struct {
			Executor              bool     `json:"executor"`
			ModelProvider         bool     `json:"model_provider"`
			ModelRouter           bool     `json:"model_router"`
			ManagementAPI         bool     `json:"management_api"`
			UsagePlugin           bool     `json:"usage_plugin"`
			ExecutorInputFormats  []string `json:"executor_input_formats"`
			ExecutorOutputFormats []string `json:"executor_output_formats"`
		} `json:"capabilities"`
	}
	if errUnmarshal := json.Unmarshal(envelopeResult(t, raw), &decoded); errUnmarshal != nil {
		t.Fatalf("registration payload is invalid: %v", errUnmarshal)
	}
	if decoded.SchemaVersion != 6 {
		t.Errorf("schema_version = %d, want 6", decoded.SchemaVersion)
	}
	if !decoded.Capabilities.Executor || !decoded.Capabilities.ModelProvider {
		t.Errorf("missing capabilities: %+v", decoded.Capabilities)
	}
	// The host picks the provider for a model id. Advertising model_router would
	// let this plugin override that choice and dead-end the request whenever a
	// native channel also serves the same id.
	if decoded.Capabilities.ModelRouter {
		t.Error("the plugin must not advertise model_router")
	}
	if !decoded.Capabilities.ManagementAPI {
		t.Error("the chat build owns the dashboard and must declare management_api")
	}
	if !decoded.Capabilities.UsagePlugin {
		t.Error("the main build must receive host usage events for the statistics tab")
	}
	if len(decoded.Capabilities.ExecutorInputFormats) != 1 || decoded.Capabilities.ExecutorInputFormats[0] != "openai" {
		t.Errorf("chat build must declare exactly the openai format: %v", decoded.Capabilities.ExecutorInputFormats)
	}
}

// The host lists one row per shared library. All four rows have to advertise
// the same product identity so they read as four faces of one plugin, and the
// icon has to be a source the management centre accepts (<img src=…>).
func TestRegistrationCarriesOneProductBranding(t *testing.T) {
	type registrationDoc struct {
		Metadata struct {
			Name             string `json:"Name"`
			Version          string `json:"Version"`
			Author           string `json:"Author"`
			GitHubRepository string `json:"GitHubRepository"`
			Logo             string `json:"Logo"`
			ConfigFields     []struct {
				Name string `json:"Name"`
			} `json:"ConfigFields"`
		} `json:"metadata"`
	}
	labels := map[string]bool{}
	for _, role := range roles.All {
		runtime := New(role)
		runtime.SetVersion("9.9.9")
		var decoded registrationDoc
		if errUnmarshal := json.Unmarshal(envelopeResult(t, runtime.registrationPayload()), &decoded); errUnmarshal != nil {
			t.Fatalf("%s: registration payload is invalid: %v", role.Name, errUnmarshal)
		}
		if decoded.Metadata.Name != role.Label {
			t.Errorf("%s: Name = %q, want %q", role.Name, decoded.Metadata.Name, role.Label)
		}
		if labels[decoded.Metadata.Name] {
			t.Errorf("%s: duplicate plugin name %q", role.Name, decoded.Metadata.Name)
		}
		labels[decoded.Metadata.Name] = true
		if decoded.Metadata.Version != "9.9.9" {
			t.Errorf("%s: Version = %q", role.Name, decoded.Metadata.Version)
		}
		if decoded.Metadata.Author != brand.Author || decoded.Metadata.GitHubRepository != brand.Repository {
			t.Errorf("%s: author/repository = %q/%q", role.Name, decoded.Metadata.Author, decoded.Metadata.GitHubRepository)
		}
		if decoded.Metadata.Logo != brand.Logo {
			t.Errorf("%s: the four faces must show the same icon", role.Name)
		}
		if !strings.HasPrefix(decoded.Metadata.Logo, "data:image/svg+xml;base64,") {
			t.Errorf("%s: Logo = %.60s", role.Name, decoded.Metadata.Logo)
		}
		if strings.ContainsAny(decoded.Metadata.Logo, "\n\r\"'") {
			t.Errorf("%s: Logo must stay a single, unescaped token", role.Name)
		}
		// Only the main face carries the configuration: the siblings read the
		// snapshot it publishes and have nothing of their own to configure.
		if role.Name == roles.Chat {
			if len(decoded.Metadata.ConfigFields) != 18 {
				t.Errorf("main face declares %d config fields, want the documented 18", len(decoded.Metadata.ConfigFields))
			}
		} else if len(decoded.Metadata.ConfigFields) != 0 {
			t.Errorf("%s: sibling faces must not advertise config fields: %v", role.Name, decoded.Metadata.ConfigFields)
		}
	}
}

// The dashboard manages the four protocol faces as one product, so the state
// payload has to describe every face: what it serves and whether it owns the
// configuration.
func TestStatePayloadDescribesEveryProtocolFace(t *testing.T) {
	runtime := New(rolesChat())
	runtime.applySettings(config.Default())

	payload := runtime.StatePayload()
	if len(payload.Siblings) != len(roles.All) {
		t.Fatalf("state describes %d faces, want %d", len(payload.Siblings), len(roles.All))
	}
	byRole := make(map[string]SiblingRow, len(payload.Siblings))
	for _, face := range payload.Siblings {
		if face.Label == "" || face.Protocol == "" || face.Endpoint == "" || face.PluginID == "" || face.Provider == "" {
			t.Errorf("face %s is incomplete: %+v", face.Role, face)
		}
		if len(face.Formats) == 0 {
			t.Errorf("face %s declares no formats", face.Role)
		}
		if len(face.Models) > faceModelSample {
			t.Errorf("face %s samples %d models, want at most %d", face.Role, len(face.Models), faceModelSample)
		}
		if face.ModelCount < len(face.Models) {
			t.Errorf("face %s reports %d models but samples %d", face.Role, face.ModelCount, len(face.Models))
		}
		byRole[face.Role] = face
	}
	main := byRole["chat"]
	if !main.Main {
		t.Error("the chat face owns the page and configuration and must be marked main")
	}
	if main.Label != brand.Name {
		t.Errorf("main face label = %q, want the bare product name", main.Label)
	}
	if main.ModelCount == 0 || len(main.Models) == 0 {
		t.Error("the main face serves the chat models and must list them")
	}
	for role, face := range byRole {
		if role != "chat" && face.Main {
			t.Errorf("face %s must not claim the configuration", role)
		}
	}
}

func TestSiblingBuildHasNoMenu(t *testing.T) {
	runtime := New(rolesGemini())
	raw := runtime.handleManagementRegister(nil)
	var decoded struct {
		Resources []struct {
			Path string `json:"Path"`
		} `json:"resources"`
	}
	if errUnmarshal := json.Unmarshal(envelopeResult(t, raw), &decoded); errUnmarshal != nil {
		t.Fatalf("management registration is invalid: %v", errUnmarshal)
	}
	if len(decoded.Resources) != 0 {
		t.Fatalf("only the main build may own the dashboard, got %d resources", len(decoded.Resources))
	}
}

func TestTerminalEventPayload(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`)

	wrapped := terminalEventPayload(raw)
	var decoded struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if errUnmarshal := json.Unmarshal(wrapped, &decoded); errUnmarshal != nil {
		t.Fatalf("wrapped payload is not JSON: %v", errUnmarshal)
	}
	if decoded.Type != "response.completed" {
		t.Errorf("type = %q, want response.completed", decoded.Type)
	}
	if string(decoded.Response) != string(raw) {
		t.Errorf("the original body must be embedded unchanged, got %s", decoded.Response)
	}

	// An incomplete response keeps its status so the client sees the right reason.
	incomplete := terminalEventPayload([]byte(`{"status":"incomplete","object":"response"}`))
	if !strings.Contains(string(incomplete), `"response.incomplete"`) {
		t.Errorf("incomplete responses must map to response.incomplete: %s", incomplete)
	}

	// An event payload is already in the expected shape and must pass through.
	event := []byte(`{"type":"response.completed","response":{"id":"resp_2"}}`)
	if got := terminalEventPayload(event); string(got) != string(event) {
		t.Errorf("event payloads must be untouched, got %s", got)
	}

	// Non-object bodies pass through untouched.
	for _, body := range [][]byte{nil, []byte(""), []byte("not json")} {
		if got := terminalEventPayload(body); string(got) != string(body) {
			t.Errorf("body %q should pass through, got %s", body, got)
		}
	}
}

// An alias replaces the model id, so the host must be told about the alias only:
// registering both would put the id the alias renamed back into /v1/models.
func TestModelsAreRegisteredUnderTheAliasOnly(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Models = map[string]config.ModelSetting{
		"deepseek-v4.1-flash": {Aliases: []string{"DeepSeek-V4.1-Flash"}},
		"grok-4.7":            {Aliases: []string{"grok"}},
	}
	runtime.applySettings(settings)

	var decoded struct {
		Models []struct {
			ID   string `json:"ID"`
			Name string `json:"Name"`
		} `json:"Models"`
	}
	if errUnmarshal := json.Unmarshal(envelopeResult(t, runtime.handleStaticModels(nil)), &decoded); errUnmarshal != nil {
		t.Fatalf("model response is invalid: %v", errUnmarshal)
	}
	registered := make(map[string]string, len(decoded.Models))
	for _, model := range decoded.Models {
		registered[model.ID] = model.Name
	}

	if _, exists := registered["deepseek-v4.1-flash"]; exists {
		t.Error("the renamed model id must not be registered")
	}
	name, exists := registered["DeepSeek-V4.1-Flash"]
	if !exists {
		t.Fatal("the alias must be registered for the chat build")
	}
	// The host prints Name in the Gemini model list, so it has to be the
	// client-facing id rather than the upstream one.
	if name != "DeepSeek-V4.1-Flash" {
		t.Errorf("Name = %q, want the client-facing id", name)
	}
	if _, exists := registered["grok"]; exists {
		t.Error("an alias must inherit the protocol family of its model")
	}
}

// The dashboard edits configuration per real model, so a renamed model must stay
// one row carrying every id it answers to.
func TestModelRowsGroupAliasedModels(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Models = map[string]config.ModelSetting{
		"deepseek-v4.1-flash": {Aliases: []string{"DeepSeek-V4.1-Flash"}},
		// A rejected alias: the id belongs to the model above.
		"grok-4.7": {Aliases: []string{"deepseek-v4.1-flash"}},
	}
	runtime.applySettings(settings)

	rows := runtime.modelRows(nil)
	byID := make(map[string]ModelRow, len(rows))
	for _, row := range rows {
		if _, duplicate := byID[row.ID]; duplicate {
			t.Fatalf("duplicate row for %s", row.ID)
		}
		byID[row.ID] = row
	}

	renamed, ok := byID["deepseek-v4.1-flash"]
	if !ok {
		t.Fatal("the renamed model lost its row")
	}
	if !renamed.Aliased || len(renamed.ClientIDs) != 1 || renamed.ClientIDs[0] != "DeepSeek-V4.1-Flash" {
		t.Errorf("renamed row = %+v", renamed)
	}
	if _, exists := byID["DeepSeek-V4.1-Flash"]; exists {
		t.Error("an alias must not get a row of its own any more")
	}

	loser, ok := byID["grok-4.7"]
	if !ok {
		t.Fatal("the model with the rejected alias lost its row")
	}
	if loser.Aliased || len(loser.ClientIDs) != 1 || loser.ClientIDs[0] != "grok-4.7" {
		t.Errorf("a model with no usable alias must keep its own id: %+v", loser)
	}
	if !loser.AliasShadowed || len(loser.ShadowedAliases) != 1 || loser.ShadowedAliases[0] != "deepseek-v4.1-flash" {
		t.Errorf("the rejection must be reported on the declaring row: %+v", loser)
	}
}

// The live upstream list still publishes the id an alias renamed; it must not
// come back as a second, "discovered" row for the same model.
func TestModelRowsDoNotRediscoverRenamedModels(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Models = map[string]config.ModelSetting{
		"deepseek-v4.1-flash": {Aliases: []string{"DeepSeek-V4.1-Flash"}},
	}
	runtime.applySettings(settings)

	var renamed, discovered int
	for _, row := range runtime.modelRows([]string{"deepseek-v4.1-flash", "brand-new-model"}) {
		switch row.ID {
		case "deepseek-v4.1-flash":
			renamed++
			if row.Discovered {
				t.Error("the renamed model must not be reported as discovered")
			}
			if !row.UpstreamAvailable {
				t.Error("the upstream still publishes the model, so it must count as available")
			}
		case "DeepSeek-V4.1-Flash":
			t.Error("an alias must never be a row of its own")
		case "brand-new-model":
			discovered++
			if !row.Discovered {
				t.Error("an id nobody serves must be reported as discovered")
			}
		}
	}
	if renamed != 1 || discovered != 1 {
		t.Errorf("renamed rows = %d, discovered rows = %d; want 1 and 1", renamed, discovered)
	}
}

// The dashboard is editable without a management credential, so every setting it
// can write has to be readable from the credential-free state payload — and the
// payload must never carry a stored secret.
func TestStatePayloadExposesEditableSettingsWithoutSecrets(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.RequestTimeoutSeconds = 321
	settings.Debug = true
	settings.StateDir = "/tmp/opencode-go-state"
	settings.Keys = []config.Key{{Name: "main", Key: "oc_sk_super_secret_value_1234", Priority: 2, Enabled: boolPointer(true)}}
	runtime.applySettings(settings)

	payload := runtime.StatePayload()
	if payload.TimeoutSeconds != 321 {
		t.Errorf("timeout = %d, want 321", payload.TimeoutSeconds)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].Priority != 2 {
		t.Errorf("keys payload = %+v", payload.Keys)
	}
	if !payload.Debug || payload.StateDir != "/tmp/opencode-go-state" {
		t.Errorf("debug/state dir = %v/%q", payload.Debug, payload.StateDir)
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}
	if strings.Contains(string(raw), "oc_sk_super_secret_value_1234") {
		t.Error("the state payload must never contain a key secret")
	}
	if !strings.Contains(string(raw), "oc_sk_su") {
		t.Error("the masked key should still be reported so the page can identify it")
	}
}

func boolPointer(value bool) *bool { return &value }

// Only the main face subscribes to usage events; four subscriptions would
// multiply-count the same request if the host delivered to each face.
func TestOnlyMainFaceRegistersUsagePlugin(t *testing.T) {
	for _, role := range roles.All {
		runtime := New(role)
		var decoded struct {
			Capabilities struct {
				UsagePlugin bool `json:"usage_plugin"`
			} `json:"capabilities"`
		}
		if errUnmarshal := json.Unmarshal(envelopeResult(t, runtime.registrationPayload()), &decoded); errUnmarshal != nil {
			t.Fatalf("%s: %v", role.Name, errUnmarshal)
		}
		want := role.Name == roles.Chat
		if decoded.Capabilities.UsagePlugin != want {
			t.Errorf("%s: usage_plugin = %v, want %v", role.Name, decoded.Capabilities.UsagePlugin, want)
		}
	}
}

func TestStatePayloadCarriesPlan(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Plan = "go-plus"
	runtime.applySettings(settings)
	if got := runtime.StatePayload().Plan; got != "go-plus" {
		t.Errorf("state plan = %q", got)
	}
}

func TestStatePayloadReportsAccountProjection(t *testing.T) {
	dir := t.TempDir()
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Keys = []config.Key{
		{Name: "main", Key: "oc_sk_main"},
		{Name: "backup", Key: "oc_sk_backup"},
	}
	runtime.applySettings(settings)
	runtime.rememberHostSummary(pluginapi.HostConfigSummary{AuthDir: dir})
	runtime.syncAccountsAsync()
	waitForSync(t, runtime)
	t.Cleanup(runtime.Shutdown)

	payload := runtime.StatePayload()
	if !payload.Accounts.Enabled || !payload.Accounts.Synced {
		t.Fatalf("accounts payload = %+v", payload.Accounts)
	}
	if payload.Accounts.Total != 8 {
		t.Fatalf("accounts total = %d, want 8", payload.Accounts.Total)
	}
	if payload.Accounts.Files["main"] != "opencode-go-main.json" {
		t.Fatalf("accounts files = %v", payload.Accounts.Files)
	}
	if payload.Accounts.AuthDir != dir {
		t.Fatalf("accounts auth dir = %q, want %q", payload.Accounts.AuthDir, dir)
	}
}
