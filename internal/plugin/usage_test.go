package plugin

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

func newStatsTestRuntime(t *testing.T, role roles.Name) *Runtime {
	t.Helper()
	runtime := New(roles.ByName(string(role)))
	runtime.sharedPath = filepath.Join(t.TempDir(), "shared.json")
	runtime.applySettings(config.Default())
	t.Cleanup(runtime.Shutdown)
	return runtime
}

func sendUsage(t *testing.T, runtime *Runtime, record pluginapi.UsageRecord) {
	t.Helper()
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatalf("marshal usage record: %v", errMarshal)
	}
	envelopeResult(t, runtime.handleUsage(raw))
}

func usageAt() time.Time {
	// Saturday: DeepSeek off-peak, so the expected cost is deterministic.
	return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
}

func TestUsageProviderFilter(t *testing.T) {
	accepted := []pluginapi.UsageRecord{
		{Provider: "cpa-opencodego", Model: "glm-5.3"},
		{Provider: "cpa-opencodego-messages", Model: "glm-5.3"},
		{Provider: "opencode-go-gemini", Model: "glm-5.3"},
		{Source: "cpa-opencodego-responses", Model: "glm-5.3"},
		{ExecutorType: "plugin:cpa-opencodego", Model: "glm-5.3"},
	}
	for _, record := range accepted {
		if !ownsUsageRecord(record) {
			t.Errorf("record %+v must be accepted", record)
		}
	}
	for _, record := range []pluginapi.UsageRecord{
		{Provider: "openai", Model: "gpt-5"},
		{Provider: "anthropic", Source: "claude-code", Model: "claude"},
		{},
	} {
		if ownsUsageRecord(record) {
			t.Errorf("foreign record %+v must be ignored", record)
		}
	}
}

func TestUsageRecordsOnlyOwnProvider(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego",
		Model:    "glm-5.3",
		Detail:   pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
	})
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "some-other-provider",
		Model:    "gpt-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 100},
	})
	snapshot := runtime.statsStore().Snapshot(time.Time{})
	if snapshot.Received != 2 || snapshot.Matched != 1 {
		t.Errorf("received/matched = %d/%d, want 2/1", snapshot.Received, snapshot.Matched)
	}
	if _, exists := snapshot.Models["gpt-5"]; exists {
		t.Error("a foreign provider's model must not enter the counters")
	}
}

func TestUsageResolvesAliasAndPrices(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	settings := config.Default()
	settings.Models = map[string]config.ModelSetting{
		"deepseek-v4.1-flash": {Aliases: []string{"DeepSeek-V4.1-Flash"}},
	}
	runtime.applySettings(settings)

	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider:    "cpa-opencodego",
		Model:       "DeepSeek-V4.1-Flash",
		RequestedAt: usageAt(),
		Stream:      true,
		Latency:     2 * time.Second,
		Detail: pluginapi.UsageDetail{
			InputTokens: 1_000_000, OutputTokens: 1_000_000,
			TotalTokens: 2_000_000,
		},
	})
	snapshot := runtime.statsStore().Snapshot(time.Time{})
	model, exists := snapshot.Models["deepseek-v4.1-flash"]
	if !exists {
		t.Fatalf("alias did not resolve to the base id: %+v", snapshot.Models)
	}
	if model.Family != "chat" {
		t.Errorf("family = %q, want chat", model.Family)
	}
	if model.Counter.CostNanoUSD != 750_000_000 {
		t.Errorf("cost = %d nanoUSD, want 750000000", model.Counter.CostNanoUSD)
	}
	if model.Counter.Priced != 1 || model.Counter.Streams != 1 || model.Counter.StreamOutput != 1_000_000 {
		t.Errorf("counter = %+v", model.Counter)
	}
	if model.Counter.LatencyOutput != 1_000_000 || model.Counter.LatencyNano != int64(2*time.Second) || model.Counter.LatencySamples != 1 {
		t.Errorf("paired TPS counter = %+v", model.Counter)
	}
}

func TestUsageMessagesCacheConvention(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider:    "cpa-opencodego-messages",
		Model:       "claude-haiku-5-5",
		RequestedAt: usageAt(),
		Detail: pluginapi.UsageDetail{
			InputTokens: 1_000, CacheReadTokens: 100_000, CacheCreationTokens: 1_000,
			OutputTokens: 1_000, TotalTokens: 103_000,
		},
	})
	model := runtime.statsStore().Snapshot(time.Time{}).Models["claude-haiku-5-5"]
	if model.Family != "messages" {
		t.Errorf("family = %q, want messages", model.Family)
	}
	// Prompt 102K > 100K: 1K input, 100K read, 1K write, 1K output at the
	// high Anthropic tier.
	if model.Counter.CostNanoUSD != 8_625_000 {
		t.Errorf("messages cost = %d nanoUSD, want 8625000", model.Counter.CostNanoUSD)
	}
}

func TestUsageGeminiAddsReasoningToOutputCost(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider:    "cpa-opencodego-gemini",
		Model:       "gemini-4-argon",
		RequestedAt: usageAt(),
		Latency:     4 * time.Second,
		Detail: pluginapi.UsageDetail{
			OutputTokens: 1_000, ReasoningTokens: 1_000, TotalTokens: 2_000,
		},
	})
	model := runtime.statsStore().Snapshot(time.Time{}).Models["gemini-4-argon"]
	if model.Counter.CostNanoUSD != 40_000_000 {
		t.Errorf("gemini cost = %d nanoUSD, want 40000000", model.Counter.CostNanoUSD)
	}
	if model.Counter.LatencyOutput != 2_000 || model.Counter.LatencyNano != int64(4*time.Second) {
		t.Errorf("gemini TPS output/latency = %+v, want reasoning included", model.Counter)
	}
}

// Historical rows written before the Gemini rename still carry the old id and
// must keep the Gemini billing rule (reasoning billed as output), even though
// the id is no longer in the catalogue.
func TestUsageLegacyGeminiIDStillCountsAsGemini(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider:    "cpa-opencodego-gemini",
		Model:       "gemini-4-argon-contributor",
		RequestedAt: usageAt(),
		Detail: pluginapi.UsageDetail{
			OutputTokens: 1_000, ReasoningTokens: 1_000, TotalTokens: 2_000,
		},
	})
	model := runtime.statsStore().Snapshot(time.Time{}).Models["gemini-4-argon-contributor"]
	if model.Counter.CostNanoUSD != 40_000_000 {
		t.Errorf("legacy gemini cost = %d nanoUSD, want 40000000", model.Counter.CostNanoUSD)
	}
}

// The chat face's suffix prefixes every companion face, so role detection must
// pick the longest match instead of the first one in roles.All.
func TestUsageRolePrefersMostSpecificFace(t *testing.T) {
	cases := map[string]roles.Name{
		"cpa-opencodego":                     roles.Chat,
		"opencode-go":                        roles.Chat,
		"plugin:cpa-opencodego":              roles.Chat,
		"cpa-opencodego-gemini":              roles.Gemini,
		"opencode-go-gemini":                 roles.Gemini,
		"plugin:cpa-opencodego-messages":     roles.Messages,
		"cpa-opencodego-responses":           roles.Responses,
		"cpa-opencodego-gemini-extra-suffix": roles.Gemini,
		"unrelated-provider":                 "",
	}
	for input, want := range cases {
		got, ok := usageRole(input)
		if want == "" {
			if ok {
				t.Errorf("usageRole(%q) = %q, want no match", input, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("usageRole(%q) = (%q, %v), want %q", input, got, ok, want)
		}
	}
}

func TestUsageFreeAndUnpricedModels(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego", Model: "longcat-2.5-preview-free",
		Detail: pluginapi.UsageDetail{InputTokens: 1_000},
	})
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego", Model: "omen-alpha",
		Detail: pluginapi.UsageDetail{InputTokens: 1_000},
	})
	snapshot := runtime.statsStore().Snapshot(time.Time{})
	free := snapshot.Models["longcat-2.5-preview-free"]
	if free.Counter.CostNanoUSD != 0 {
		t.Errorf("free model cost = %d", free.Counter.CostNanoUSD)
	}
	if free.Counter.Priced != 1 {
		t.Errorf("free model must count as priced: %+v", free.Counter)
	}
	unknown := snapshot.Models["omen-alpha"]
	if unknown.Counter.Unpriced != 1 || unknown.Counter.Priced != 0 {
		t.Errorf("unpriced counters = %+v", unknown.Counter)
	}
}

func TestUsageFallsBackToAliasAndResponseModel(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego", Alias: "deepseek-v4.1-flash",
		Detail: pluginapi.UsageDetail{InputTokens: 10},
	})
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego", ResponseModel: "glm-5.3",
		Detail: pluginapi.UsageDetail{InputTokens: 10},
	})
	snapshot := runtime.statsStore().Snapshot(time.Time{})
	if _, exists := snapshot.Models["deepseek-v4.1-flash"]; !exists {
		t.Error("the alias must be used when Model is empty")
	}
	if _, exists := snapshot.Models["glm-5.3"]; !exists {
		t.Error("the response model must be used as a fallback")
	}
}

func TestUsageBareModelNameStripsThinkingSuffix(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	sendUsage(t, runtime, pluginapi.UsageRecord{
		Provider: "cpa-opencodego", Model: "glm-5.3(high)",
		Detail: pluginapi.UsageDetail{InputTokens: 10},
	})
	if _, exists := runtime.statsStore().Snapshot(time.Time{}).Models["glm-5.3"]; !exists {
		t.Error("the thinking suffix must be stripped for the stats key")
	}
}

func TestUsageMalformedRecordDoesNotFail(t *testing.T) {
	runtime := newStatsTestRuntime(t, roles.Chat)
	result := runtime.handleUsage([]byte("{"))
	envelopeResult(t, result)
	if got := runtime.statsStore().Snapshot(time.Time{}).Received; got != 1 {
		t.Errorf("received = %d, want 1", got)
	}
}

// CPA reports the projected auth file id in usage events; every protocol face
// must map back to the same configured key so per-key statistics stay correct.
func TestResolveKeyNameFromProjectedAuthID(t *testing.T) {
	runtime := New(rolesChat())
	settings := config.Default()
	settings.Keys = []config.Key{
		{Name: "main", Key: "sk-main"},
		{Name: "backup", Key: "sk-backup"},
	}
	runtime.applySettings(settings)

	cases := map[string]string{
		"opencode-go-main.json":                        "main",
		"/root/.cli-proxy-api/opencode-go-backup.json": "backup",
		"opencode-go-main--responses.json":             "main",
		"opencode-go-backup--messages.json":            "backup",
		"opencode-go-main--gemini.json":                "main",
		"":                                             "",
		"codex-account.json":                           "",
	}
	for authID, want := range cases {
		if got := runtime.resolveKeyName(authID); got != want {
			t.Errorf("resolveKeyName(%q) = %q, want %q", authID, got, want)
		}
	}
}
