package plugin

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/accounts"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/models"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/pricing"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/stats"
)

// handleUsage receives the host's usage events. The host already resolved the
// protocol-specific token fields, so the plugin does not parse responses or
// inject stream_options; it only filters to its own four faces, resolves the
// model to the configured id, prices the tokens, and accumulates counters.
//
// A malformed or foreign record must never fail the call: the host fuses a
// plugin whose usage handler returns an error.
func (r *Runtime) handleUsage(request []byte) []byte {
	store := r.statsStore()
	if store == nil {
		return okEmpty()
	}
	if len(request) == 0 {
		store.NoteRecord(false)
		return okEmpty()
	}
	var record pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(request, &record); errUnmarshal != nil {
		store.NoteRecord(false)
		r.trace("usage record ignored", map[string]any{"error": errUnmarshal.Error(), "bytes": len(request)})
		return okEmpty()
	}
	if !ownsUsageRecord(record) {
		store.NoteRecord(false)
		r.trace("usage record not ours", map[string]any{
			"provider": record.Provider, "source": record.Source,
			"executor": record.ExecutorType, "model": record.Model,
		})
		return okEmpty()
	}
	sample, ok := r.usageSample(record)
	if !ok {
		store.NoteRecord(false)
		r.trace("usage record has no model", map[string]any{
			"provider": record.Provider, "source": record.Source, "model": record.Model, "alias": record.Alias,
		})
		return okEmpty()
	}
	store.NoteRecord(true)
	store.ObserveUsage(sample)
	return okEmpty()
}

// ownsUsageRecord accepts the labels the host and the token tracker use for
// this plugin's four provider faces. The live host reports the plugin id
// (cpa-opencodego*) rather than executor.identifier's provider key, so both
// spellings, and the "plugin:" prefix some reports carry, are accepted.
func ownsUsageRecord(record pluginapi.UsageRecord) bool {
	for _, value := range []string{record.Provider, record.Source, record.ExecutorType} {
		if _, ok := usageRole(value); ok {
			return true
		}
	}
	return false
}

func usageRole(value string) (roles.Name, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.TrimPrefix(trimmed, "plugin:")
	if trimmed == "" {
		return "", false
	}
	// The chat face's plugin suffix ("cpa-opencodego") is a string prefix of
	// every companion face ("cpa-opencodego-gemini", …). Matching role by role
	// in declaration order would therefore attribute Gemini/Messages/Responses
	// usage to Chat, which also decides whether reasoning tokens are billed as
	// output. Prefer an exact id, then the longest matching prefix.
	best := roles.Name("")
	bestLen := -1
	for _, role := range roles.All {
		for _, candidate := range []string{role.Provider, role.PluginSuffix} {
			lowered := strings.ToLower(candidate)
			if lowered == "" {
				continue
			}
			if trimmed == lowered {
				return role.Name, true
			}
			if strings.HasPrefix(trimmed, lowered+"-") && len(lowered) > bestLen {
				best = role.Name
				bestLen = len(lowered)
			}
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	return "", false
}

// usageSample normalizes one record into the counters the store understands.
func (r *Runtime) usageSample(record pluginapi.UsageRecord) (stats.UsageSample, bool) {
	resolver := r.Resolver()
	candidates := []string{
		bareModelName(record.Model),
		bareModelName(record.Alias),
		bareModelName(record.ResponseModel),
	}
	model := ""
	family := roles.Name("")
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if entry, ok := resolver.Resolve(candidate); ok {
			model = entry.BaseID
			family = entry.Family
			break
		}
		if model == "" {
			model = candidate
		}
	}
	if model == "" {
		return stats.UsageSample{}, false
	}
	if family == "" {
		for _, value := range []string{record.Provider, record.Source, record.ExecutorType} {
			if role, ok := usageRole(value); ok {
				family = role
				break
			}
		}
	}
	if family == "" {
		family = models.FamilyFor(model)
	}

	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}
	detail := record.Detail
	tokens := pricing.Tokens{
		Input:         detail.InputTokens,
		Output:        detail.OutputTokens,
		Reasoning:     detail.ReasoningTokens,
		Cached:        detail.CachedTokens,
		CacheRead:     detail.CacheReadTokens,
		CacheCreation: detail.CacheCreationTokens,
	}
	total := detail.TotalTokens
	if total == 0 {
		total = tokens.Input + tokens.Output + tokens.CacheRead + tokens.CacheCreation
	}
	unmetered := tokens.Input == 0 && tokens.Output == 0 && tokens.Reasoning == 0 &&
		tokens.Cached == 0 && tokens.CacheRead == 0 && tokens.CacheCreation == 0
	costNano, priced := pricing.Cost(model, tokens, family != roles.Messages, family == roles.Gemini, at)
	tpsOutput := tokens.Output
	if family == roles.Gemini && tokens.Reasoning > 0 {
		// Gemini reports thoughts separately from visible output, but they are
		// generated during the same request latency and belong in throughput.
		tpsOutput += tokens.Reasoning
	}
	if tpsOutput < 0 {
		tpsOutput = 0
	}

	keyName := r.resolveKeyName(record.AuthID)

	return stats.UsageSample{
		Model:         model,
		Family:        string(family),
		Key:           keyName,
		At:            at,
		Input:         tokens.Input,
		Output:        tokens.Output,
		Reasoning:     tokens.Reasoning,
		Cached:        tokens.Cached,
		CacheRead:     tokens.CacheRead,
		CacheCreation: tokens.CacheCreation,
		Total:         total,
		CostNanoUSD:   costNano,
		Priced:        priced,
		Stream:        record.Stream,
		Failed:        record.Failed,
		Unmetered:     unmetered,
		Latency:       record.Latency,
		TPSOutput:     tpsOutput,
	}, true
}

// resolveKeyName maps the host's auth id (the projected auth file name) back to
// the configured key, so per-key statistics stay correct when CPA schedules more
// than one key. The name fallback covers hosts that report a label instead of a
// file id.
func (r *Runtime) resolveKeyName(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	planned := accounts.Plan(r.Settings().Keys)
	if name, _, ok := accounts.KeyNameFromAuthID(authID, planned); ok {
		return name
	}
	for _, k := range r.Settings().Keys {
		if k.Name != "" && (authID == k.Name || strings.Contains(authID, k.Name)) {
			return k.Name
		}
	}
	return ""
}
