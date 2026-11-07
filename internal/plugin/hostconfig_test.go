package plugin

import (
	"strings"
	"testing"
)

const v8ConfigFixture = `
requests:
    proxy-url: "http://127.0.0.1:7890"
api-keys:
    gemini:
    -   name: opencode-gemini-direct
        base-url: https://opencode.ai/inference/go/google
        priority: 10
        proxy-url: direct
        headers:
            x-opencode-session: $CPA-SESSION-ID
        keys:
        -   api-key: oc_sk_alpha_1111
        -   api-key: oc_sk_beta_2222
        models:
        -   name: gemini-4-argon
            max-context-length: 1000000
    openai-compatibility:
    -   name: some-other-vendor
        base-url: https://api.example.com/v1
        keys:
        -   api-key: sk_other_9999
        models:
        -   name: other-model
    -   name: opencode-chat-direct
        base-url: https://opencode.ai/inference/go/v1
        priority: 10
        headers:
            x-opencode-session: $CPA-SESSION-ID
        keys:
        -   api-key: oc_sk_alpha_1111
            proxy-url: direct
        -   api-key: oc_sk_beta_2222
            proxy-url: direct
        models:
        -   name: deepseek-v4-pro
            alias: DeepSeek-V4-Pro
    -   name: opencode-chat-proxy
        base-url: https://opencode.ai/inference/go/v1
        priority: 10
        keys:
        -   api-key: oc_sk_alpha_1111
        -   api-key: oc_sk_gamma_3333
        models:
        -   name: beam-free
            alias: Beam
    -   name: opencode-legacy-disabled
        base-url: https://opencode.ai/zen/go/v1
        disabled: true
        keys:
        -   api-key: oc_sk_ghost_4444
        models:
        -   name: space-bunny-free
`

func TestParseHostConfigV8(t *testing.T) {
	config := defaultConfig()
	config.normalize()
	parsed, errParse := parseHostConfig([]byte(v8ConfigFixture), "/tmp/config.yaml", config)
	if errParse != nil {
		t.Fatalf("parseHostConfig: %v", errParse)
	}

	// The non-OpenCode compatibility group must be dropped.
	for _, group := range parsed.Groups {
		if strings.Contains(group.BaseURL, "example.com") {
			t.Fatalf("non-OpenCode group leaked into the projection: %+v", group)
		}
	}
	if len(parsed.Groups) != 4 {
		t.Fatalf("groups = %d, want 4: %+v", len(parsed.Groups), parsed.Groups)
	}
	if parsed.GlobalProxy != "http://127.0.0.1:7890" {
		t.Fatalf("global proxy = %q", parsed.GlobalProxy)
	}
	for _, group := range parsed.Groups {
		switch group.Name {
		case "opencode-gemini-direct":
			if group.Egress != "direct" {
				t.Fatalf("gemini direct group egress = %q", group.Egress)
			}
		case "opencode-chat-direct":
			// proxy-url sits on the keys in this shape, so the group row must
			// report the decision the keys make.
			if group.Egress != "direct" {
				t.Fatalf("chat direct group egress = %q", group.Egress)
			}
		case "opencode-chat-proxy":
			if group.Egress != "" {
				t.Fatalf("chat proxy group egress = %q, want inherit", group.Egress)
			}
		}
	}
	if parsed.UsageURL != "https://opencode.ai/inference/go/v1/usage" {
		t.Fatalf("usage url = %q", parsed.UsageURL)
	}

	// Three routed keys: the disabled legacy group only lists a ghost key.
	if len(parsed.Credentials) != 3 {
		t.Fatalf("credentials = %d, want 3: %+v", len(parsed.Credentials), parsed.Credentials)
	}
	byMasked := map[string]Credential{}
	for _, credential := range parsed.Credentials {
		byMasked[credential.Key] = credential
		if credential.Masked == "" || strings.Contains(credential.Masked, credential.Key) {
			t.Fatalf("credential mask leaked the key: %+v", credential)
		}
	}
	alpha, okAlpha := byMasked["oc_sk_alpha_1111"]
	if !okAlpha {
		t.Fatalf("alpha credential missing: %+v", parsed.Credentials)
	}
	// Alpha appears in a direct group, so the usage probe is direct.
	if alpha.Egress != "" {
		t.Fatalf("alpha egress = %q, want direct", alpha.Egress)
	}
	if len(alpha.GroupKeys) != 3 {
		t.Fatalf("alpha groups = %v, want 3", alpha.GroupKeys)
	}
	beta := byMasked["oc_sk_beta_2222"]
	if beta.Egress != "" {
		t.Fatalf("beta egress = %q, want direct", beta.Egress)
	}
	// Gamma only appears in an inheriting group, so it uses the host global proxy.
	gamma := byMasked["oc_sk_gamma_3333"]
	if gamma.Egress != "http://127.0.0.1:7890" {
		t.Fatalf("gamma egress = %q, want the global proxy", gamma.Egress)
	}
	_, ghost := byMasked["oc_sk_ghost_4444"]
	if ghost {
		t.Fatal("a disabled group contributed a credential")
	}
}

const legacyConfigFixture = `
gemini-api-key:
  - api-key: legacy_gemini_key_0001
    base-url: https://opencode.ai/inference/go/google
    proxy-url: direct
`

func TestParseHostConfigLegacyFamilies(t *testing.T) {
	config := defaultConfig()
	config.normalize()
	parsed, errParse := parseHostConfig([]byte(legacyConfigFixture), "/tmp/config.yaml", config)
	if errParse != nil {
		t.Fatalf("parseHostConfig: %v", errParse)
	}
	if len(parsed.Credentials) != 1 || parsed.Credentials[0].Key != "legacy_gemini_key_0001" {
		t.Fatalf("legacy credential missing: %+v", parsed.Credentials)
	}
	if parsed.Credentials[0].Egress != "" {
		t.Fatalf("legacy egress = %q, want direct", parsed.Credentials[0].Egress)
	}
}

func TestParseHostConfigNoGroups(t *testing.T) {
	config := defaultConfig()
	config.normalize()
	parsed, errParse := parseHostConfig([]byte("api-keys: {}\n"), "/tmp/config.yaml", config)
	if errParse != nil {
		t.Fatalf("parseHostConfig: %v", errParse)
	}
	if len(parsed.Groups) != 0 || len(parsed.Credentials) != 0 {
		t.Fatalf("expected an empty projection, got %+v", parsed)
	}
	if len(parsed.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one", parsed.Warnings)
	}
}

func TestUsageURLForBase(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://opencode.ai/inference/go/v1", "https://opencode.ai/inference/go/v1/usage"},
		{"https://opencode.ai/inference/go/google", "https://opencode.ai/inference/go/v1/usage"},
		{"https://opencode.ai/inference/go/openai/v1", "https://opencode.ai/inference/go/v1/usage"},
		{"https://opencode.ai/inference/go/anthropic", "https://opencode.ai/inference/go/v1/usage"},
		{"https://opencode.ai/zen/go/v1/", "https://opencode.ai/zen/go/v1/usage"},
		{"https://gateway.example.com/api/v1", "https://gateway.example.com/api/v1/usage"},
		{"https://gateway.example.com", "https://gateway.example.com/usage"},
		{"not-a-url", ""},
	}
	for _, testCase := range cases {
		if got := usageURLForBase(testCase.base); got != testCase.want {
			t.Errorf("usageURLForBase(%q) = %q, want %q", testCase.base, got, testCase.want)
		}
	}
}

func TestResolveEgressModes(t *testing.T) {
	global := "http://127.0.0.1:7890"
	config := defaultConfig()
	config.normalize()

	if got := resolveEgress([]string{"direct", ""}, config, global); got != "" {
		t.Fatalf("auto with a direct ref = %q, want direct", got)
	}
	if got := resolveEgress([]string{"", ""}, config, global); got != global {
		t.Fatalf("auto without refs = %q, want the global proxy", got)
	}
	directConfig := defaultConfig()
	directConfig.Egress = egressDirect
	directConfig.normalize()
	if got := resolveEgress([]string{global}, directConfig, global); got != "" {
		t.Fatalf("forced direct = %q", got)
	}
	proxyConfig := defaultConfig()
	proxyConfig.Egress = egressProxy
	proxyConfig.ProxyURL = "socks5://127.0.0.1:1080"
	proxyConfig.normalize()
	if got := resolveEgress(nil, proxyConfig, global); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("forced proxy = %q", got)
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"./CLIProxyAPI", "-config", "/CLIProxyAPI/conf/config.yaml"}, "/CLIProxyAPI/conf/config.yaml"},
		{[]string{"./CLIProxyAPI", "--config=/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{[]string{"./CLIProxyAPI"}, ""},
		{[]string{"./CLIProxyAPI", "-config"}, ""},
	}
	for _, testCase := range cases {
		if got := configPathFromArgs(testCase.args); got != testCase.want {
			t.Errorf("configPathFromArgs(%v) = %q, want %q", testCase.args, got, testCase.want)
		}
	}
}

func TestMaskKeyNeverRendersTheWholeSecret(t *testing.T) {
	if got := maskKey("short"); got != "shor…" {
		t.Fatalf("maskKey(short) = %q", got)
	}
	key := "oc_sk_cd1a5fe584e5_qGE0_l1zG8RGDWBl96aGuJfCljxrq2N4"
	masked := maskKey(key)
	if strings.Contains(masked, key) || !strings.HasPrefix(masked, "oc_sk_cd1a") || !strings.HasSuffix(masked, "q2N4") {
		t.Fatalf("maskKey = %q", masked)
	}
	if credentialID(key) == credentialID(key+"x") {
		t.Fatal("credentialID collided")
	}
}
