// Package pricing holds the OpenCode Go list-price snapshot and the plan
// monthly limits used by the 统计 tab.
//
// The numbers are an embedded snapshot, not a live feed: they come from
// https://opencode.ai/docs/go/ (the Go and Go Plus tables) and, for model ids
// the Go list has already retired, from https://opencode.ai/docs/console/models/
// (October 2026 revision). A changed upstream price needs a new build; the
// dashboard always shows the revision so a stale estimate is recognisable.
//
// Cost is an estimate of what the same tokens would cost at list price. It is
// not the operator's OpenCode bill.
package pricing

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Revision identifies the embedded price table.
const Revision = "2026-10"

// SnapshotCheckedAt is the UTC date on which the embedded table was last
// compared with the official Go pricing page. It is deliberately a date, not
// build time: a rebuild that does not re-check the upstream table must not make
// stale prices look fresh.
const SnapshotCheckedAt = "2026-10-17"

// RefreshAfterDays is the dashboard's reminder cadence for re-checking the
// upstream price table. OpenCode changes model lists, limited-time limits and
// token rates outside the plugin's release cycle, so a monthly nudge is safer
// than waiting for someone to remember.
const RefreshAfterDays = 30

// SourceURL documents where the Go prices and limits come from.
const SourceURL = "https://opencode.ai/docs/go/"

// ConsoleModelsURL documents the fallback table for retired model ids.
const ConsoleModelsURL = "https://opencode.ai/docs/console/models/"

// Freshness describes how old the embedded price snapshot is. UpdateDue is a
// calendar reminder, not a claim that the upstream page has changed; it only
// says the operator should look again.
type Freshness struct {
	CheckedAt time.Time
	DueAt     time.Time
	AgeDays   int
	UpdateDue bool
}

var snapshotCheckedAt = func() time.Time {
	value, err := time.Parse("2006-01-02", SnapshotCheckedAt)
	if err != nil {
		panic("pricing: invalid SnapshotCheckedAt: " + err.Error())
	}
	return value
}()

// SnapshotFreshness compares the embedded snapshot date with now in UTC. Dates
// are compared at midnight so the reminder does not flip at an arbitrary hour.
func SnapshotFreshness(now time.Time) Freshness {
	checkedAt := snapshotCheckedAt
	dueAt := checkedAt.AddDate(0, 0, RefreshAfterDays)
	nowDay := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	ageDays := int(nowDay.Sub(checkedAt).Hours() / 24)
	if ageDays < 0 {
		ageDays = 0
	}
	return Freshness{
		CheckedAt: checkedAt,
		DueAt:     dueAt,
		AgeDays:   ageDays,
		UpdateDue: !nowDay.Before(dueAt),
	}
}

// snapshotModelIDs is the upstream model list observed when the price table was
// last checked. It is deliberately separate from the built-in catalogue: the
// catalogue is what this build knows how to serve, while this list is what
// OpenCode Go actually advertised at the review date. A new or retired upstream
// id means the price book and catalogue both deserve another look, even when no
// token rate changed.
var snapshotModelIDs = []string{
	"beam-free",
	"claude-haiku-5-5",
	"deepseek-v4-pro",
	"deepseek-v4.1-flash",
	"gemini-4-argon",
	"glm-5.1",
	"glm-5.2",
	"glm-5.3",
	"glm-5.3-flash",
	"gpt-5.6-luna",
	"gpt-6-luna",
	"grok-4.6",
	"grok-4.7",
	"hy3",
	"hy4-preview",
	"kimi-k2.6",
	"kimi-k2.7-code",
	"kimi-k3",
	"longcat-2.0",
	"longcat-2.5-preview-free",
	"mimo-v2.5",
	"mimo-v2.5-pro",
	"mimo-v2.6-flash",
	"mimo-v2.6-pro",
	"minimax-m2.5",
	"minimax-m2.7",
	"minimax-m3",
	"muse-spark-1.2-contributor",
	"muse-spark-1.3-contributor",
	"omen-alpha",
	"qwen3.6-plus",
	"qwen3.7-max",
	"qwen3.7-plus",
	"qwen3.8-flash",
	"qwen3.8-max",
	"space-bunny",
	"step-5-preview",
}

// SnapshotModelIDs returns a copy of the upstream ids observed at the last
// price/catalogue review.
func SnapshotModelIDs() []string {
	return append([]string(nil), snapshotModelIDs...)
}

// ModelListCheck compares one upstream /models response with the embedded
// snapshot. An empty current list is not treated as "everything was removed":
// it usually means the fetch failed, so the zero value means "not checked".
type ModelListCheck struct {
	CheckedAt time.Time
	Added     []string
	Removed   []string
}

// Changed reports whether the upstream list differs from the snapshot.
func (c ModelListCheck) Changed() bool {
	return len(c.Added) > 0 || len(c.Removed) > 0
}

// CompareModelList diffs current upstream ids against the embedded snapshot.
// Duplicates and blank ids are ignored, and the result is sorted.
func CompareModelList(ids []string, checkedAt time.Time) ModelListCheck {
	current := modelIDSet(ids)
	if len(current) == 0 {
		return ModelListCheck{}
	}
	baseline := modelIDSet(snapshotModelIDs)
	added := make([]string, 0)
	removed := make([]string, 0)
	for id := range current {
		if _, exists := baseline[id]; !exists {
			added = append(added, id)
		}
	}
	for id := range baseline {
		if _, exists := current[id]; !exists {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return ModelListCheck{CheckedAt: checkedAt, Added: added, Removed: removed}
}

func modelIDSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

// Plan is an OpenCode Go subscription tier. Go Plus has higher monthly limits
// for the same per-token prices.
type Plan string

const (
	// PlanGo is the $10/month plan.
	PlanGo Plan = "go"
	// PlanGoPlus is the $40/month plan.
	PlanGoPlus Plan = "go-plus"
)

// NormalizePlan accepts the documented spellings and defaults to Go.
func NormalizePlan(value string) Plan {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "go-plus", "go_plus", "goplus", "plus", "go plus":
		return PlanGoPlus
	default:
		return PlanGo
	}
}

// Label is the user-facing plan name.
func (p Plan) Label() string {
	if p == PlanGoPlus {
		return "Go Plus"
	}
	return "Go"
}

// Tokens is the normalized token counters a usage record carries.
type Tokens struct {
	Input         int64
	Output        int64
	Reasoning     int64
	Cached        int64
	CacheRead     int64
	CacheCreation int64
}

// Rate is a list price in USD per 1M tokens.
type Rate struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Tier is a prompt-size band. FromPromptTokens is inclusive and the first tier
// starts at zero; tiers are sorted by FromPromptTokens.
type Tier struct {
	FromPromptTokens int64
	Rate             Rate
}

// Limits holds the monthly dollar allowance for each plan. A zero value means
// the limit is not known for that model.
type Limits struct {
	Go     float64
	GoPlus float64
}

// Entry describes one priced model.
type Entry struct {
	Model  string
	Free   bool
	Limits Limits
	Tiers  []Tier
	// Peak is the DeepSeek peak-hour rate. When set it replaces Tiers during
	// the weekday peak windows.
	Peak *Rate
}

// isPeak reports the DeepSeek peak windows: 01:00-04:00 and 06:00-10:00 UTC,
// Monday through Friday. Everything else, including weekends, is off-peak.
func isPeak(at time.Time) bool {
	utc := at.UTC()
	switch utc.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	hour := utc.Hour()
	return (hour >= 1 && hour < 4) || (hour >= 6 && hour < 10)
}

func tiered(base Rate, highFrom int64, high Rate) []Tier {
	return []Tier{
		{FromPromptTokens: 0, Rate: base},
		{FromPromptTokens: highFrom, Rate: high},
	}
}

// freeEntry is a model that is free and unlimited.
func freeEntry(model string) Entry {
	return Entry{Model: model, Free: true}
}

// legacyEntry is a model that has left the Go list but is still in the built-in
// catalogue; its token prices come from the Console list, its monthly limit is
// unknown.
func legacyEntry(model string, rate Rate) Entry {
	return Entry{Model: model, Tiers: []Tier{{FromPromptTokens: 0, Rate: rate}}}
}

// priceBook is the embedded Go table. Keep it sorted by model for readable
// diffs; the lookup map is built from it.
var priceBook = []Entry{
	// ---------------------------------------------------------- Go models
	{Model: "glm-5.3-flash", Tiers: []Tier{{Rate: Rate{Input: 0.15, Output: 0.50, CacheRead: 0.03}}}, Limits: Limits{Go: 60, GoPlus: 180}},
	{Model: "glm-5.3", Tiers: []Tier{{Rate: Rate{Input: 1.40, Output: 4.40, CacheRead: 0.26}}}, Limits: Limits{Go: 15, GoPlus: 120}},
	{Model: "glm-5.2", Tiers: []Tier{{Rate: Rate{Input: 1.40, Output: 4.40, CacheRead: 0.26}}}, Limits: Limits{Go: 60, GoPlus: 180}},
	{Model: "kimi-k3", Tiers: []Tier{{Rate: Rate{Input: 3.00, Output: 15.00, CacheRead: 0.30}}}, Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "kimi-k2.7-code", Tiers: []Tier{{Rate: Rate{Input: 0.95, Output: 4.00, CacheRead: 0.19}}}, Limits: Limits{Go: 60, GoPlus: 180}},
	{Model: "longcat-2.0", Tiers: []Tier{{Rate: Rate{Input: 0.30, Output: 1.20, CacheRead: 0.006}}}, Limits: Limits{Go: 60, GoPlus: 240}},
	freeEntry("longcat-2.5-preview-free"),
	{Model: "step-5-preview", Tiers: []Tier{{Rate: Rate{Input: 1.00, Output: 2.70, CacheRead: 0.10}}}, Limits: Limits{Go: 60, GoPlus: 240}},
	freeEntry("beam-free"),
	{Model: "mimo-v2.6-flash", Tiers: []Tier{{Rate: Rate{Input: 0.14, Output: 0.28, CacheRead: 0.0028}}}, Limits: Limits{Go: 60, GoPlus: 120}},
	{Model: "mimo-v2.6-pro", Tiers: []Tier{{Rate: Rate{Input: 0.435, Output: 0.87, CacheRead: 0.003625}}}, Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "mimo-v2.5", Tiers: []Tier{{Rate: Rate{Input: 0.14, Output: 0.28, CacheRead: 0.0028}}}, Limits: Limits{Go: 60, GoPlus: 120}},
	{Model: "mimo-v2.5-pro", Tiers: []Tier{{Rate: Rate{Input: 0.435, Output: 0.87, CacheRead: 0.003625}}}, Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "minimax-m3", Tiers: []Tier{{Rate: Rate{Input: 0.30, Output: 1.20, CacheRead: 0.06}}}, Limits: Limits{Go: 60, GoPlus: 180}},
	{Model: "muse-spark-1.3-contributor", Tiers: []Tier{{Rate: Rate{Input: 0.10, Output: 0.20, CacheRead: 0.002}}}, Limits: Limits{Go: 60, GoPlus: 120}},
	{Model: "muse-spark-1.2-contributor", Tiers: []Tier{{Rate: Rate{Input: 0.10, Output: 0.20, CacheRead: 0.002}}}, Limits: Limits{Go: 60, GoPlus: 120}},
	{Model: "qwen3.8-max", Tiers: []Tier{{Rate: Rate{Input: 2.00, Output: 6.00, CacheRead: 0.25, CacheWrite: 2.50}}}, Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "qwen3.8-flash", Tiers: []Tier{{Rate: Rate{Input: 0.15, Output: 0.47, CacheRead: 0.016, CacheWrite: 0.20}}}, Limits: Limits{Go: 30, GoPlus: 90}},
	{Model: "qwen3.7-plus", Tiers: tiered(
		Rate{Input: 0.40, Output: 1.60, CacheRead: 0.04, CacheWrite: 0.50},
		256001,
		Rate{Input: 1.20, Output: 4.80, CacheRead: 0.12, CacheWrite: 1.50},
	), Limits: Limits{Go: 60, GoPlus: 180}},
	{Model: "deepseek-v4.1-flash",
		Tiers:  []Tier{{Rate: Rate{Input: 0.15, Output: 0.60, CacheRead: 0.003}}},
		Peak:   &Rate{Input: 0.30, Output: 1.20, CacheRead: 0.006},
		Limits: Limits{Go: 80, GoPlus: 160}},
	{Model: "deepseek-v4-pro",
		Tiers:  []Tier{{Rate: Rate{Input: 0.66, Output: 1.98, CacheRead: 0.022}}},
		Peak:   &Rate{Input: 1.32, Output: 3.96, CacheRead: 0.044},
		Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "hy4-preview", Tiers: []Tier{{Rate: Rate{Input: 0.834, Output: 2.501, CacheRead: 0.042}}}, Limits: Limits{Go: 30, GoPlus: 120}},
	{Model: "hy3", Tiers: []Tier{{Rate: Rate{Input: 0.14, Output: 0.58, CacheRead: 0.035}}}, Limits: Limits{Go: 60, GoPlus: 240}},
	{Model: "space-bunny", Tiers: []Tier{{Rate: Rate{Input: 0.15, Output: 0.60, CacheRead: 0.03}}}, Limits: Limits{Go: 30, GoPlus: 120}},
	{Model: "grok-4.7", Tiers: tiered(
		Rate{Input: 2.00, Output: 6.00, CacheRead: 0.50},
		200001,
		Rate{Input: 4.00, Output: 12.00, CacheRead: 1.00},
	), Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "gpt-6-luna", Tiers: tiered(
		Rate{Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
		272001,
		Rate{Input: 0.20, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25},
	), Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "gpt-5.6-luna", Tiers: tiered(
		Rate{Input: 0.20, Output: 1.20, CacheRead: 0.02, CacheWrite: 0.25},
		272001,
		Rate{Input: 0.40, Output: 1.80, CacheRead: 0.04, CacheWrite: 0.50},
	), Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "claude-haiku-5-5", Tiers: tiered(
		Rate{Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
		100001,
		Rate{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite: 0.625},
	), Limits: Limits{Go: 15, GoPlus: 60}},
	{Model: "gemini-4-argon", Tiers: []Tier{{Rate: Rate{Input: 4.00, Output: 20.00, CacheRead: 0.20}}}, Limits: Limits{Go: 60, GoPlus: 240}},

	// ------------------------------------------------------- retired models
	// Still in the catalogue and still callable, but no longer in the Go list.
	// Prices come from the Console list; no monthly Go limit is published.
	// The old Gemini 4 Argon id was renamed; keep its rate so historical usage
	// records stay priced.
	legacyEntry("gemini-4-argon-contributor", Rate{Input: 4.00, Output: 20.00, CacheRead: 0.20}),
	legacyEntry("glm-5.1", Rate{Input: 1.40, Output: 4.40, CacheRead: 0.26}),
	legacyEntry("kimi-k2.6", Rate{Input: 0.95, Output: 4.00, CacheRead: 0.16}),
	legacyEntry("minimax-m2.5", Rate{Input: 0.30, Output: 1.20, CacheRead: 0.06}),
	legacyEntry("minimax-m2.7", Rate{Input: 0.30, Output: 1.20, CacheRead: 0.06}),
	legacyEntry("qwen3.6-plus", Rate{Input: 0.50, Output: 3.00, CacheRead: 0.05, CacheWrite: 0.625}),
	legacyEntry("qwen3.7-max", Rate{Input: 2.50, Output: 7.50, CacheRead: 0.50, CacheWrite: 3.125}),
	{Model: "grok-4.6", Tiers: tiered(
		Rate{Input: 2.00, Output: 6.00, CacheRead: 0.50},
		200001,
		Rate{Input: 4.00, Output: 12.00, CacheRead: 1.00},
	)},
	// The catalogue has always called this one free; it is not in the Go list
	// any more, so it has no monthly allowance.
	freeEntry("cat-jam-free"),
}

var lookup = buildLookup()

func buildLookup() map[string]Entry {
	out := make(map[string]Entry, len(priceBook))
	for _, entry := range priceBook {
		entry.Model = normalizeID(entry.Model)
		sort.Slice(entry.Tiers, func(i, j int) bool {
			return entry.Tiers[i].FromPromptTokens < entry.Tiers[j].FromPromptTokens
		})
		out[entry.Model] = entry
	}
	return out
}

func normalizeID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// Has reports whether the model has a list price in the embedded snapshot.
func Has(model string) bool {
	_, ok := lookup[normalizeID(model)]
	return ok
}

// IsFree reports whether the model is free (and its monthly allowance
// unlimited).
func IsFree(model string) bool {
	entry, ok := lookup[normalizeID(model)]
	return ok && entry.Free
}

// Limit returns the monthly dollar allowance for a model on a plan. unlimited
// is true for the free models; known is false when the model or its limit is
// not in the snapshot.
func Limit(model string, plan Plan) (usd float64, unlimited bool, known bool) {
	entry, ok := lookup[normalizeID(model)]
	if !ok {
		return 0, false, false
	}
	if entry.Free {
		return 0, true, true
	}
	var value float64
	if NormalizePlan(string(plan)) == PlanGoPlus {
		value = entry.Limits.GoPlus
	} else {
		value = entry.Limits.Go
	}
	if value <= 0 {
		return 0, false, false
	}
	return value, false, true
}

// Cost estimates the list-price cost of one usage record.
//
// cacheInInput follows the protocol convention: chat/completions and Gemini
// report input tokens inclusive of cached tokens, Anthropic Messages reports
// them exclusive. reasoningInOutput is true only for Gemini, where the host
// reports thoughts separately from the visible output while the price is for
// all generated tokens.
func Cost(model string, tokens Tokens, cacheInInput, reasoningInOutput bool, at time.Time) (int64, bool) {
	entry, ok := lookup[normalizeID(model)]
	if !ok {
		return 0, false
	}
	if entry.Free {
		return 0, true
	}
	rate := entry.Tiers[0].Rate
	if entry.Peak != nil && isPeak(at) {
		rate = *entry.Peak
	} else {
		prompt := promptTokens(tokens, cacheInInput)
		for _, tier := range entry.Tiers {
			if prompt >= tier.FromPromptTokens {
				rate = tier.Rate
			}
		}
	}

	cacheRead := tokens.CacheRead
	if cacheRead == 0 {
		cacheRead = tokens.Cached
	}
	cacheCreation := tokens.CacheCreation
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	input := tokens.Input
	if input < 0 {
		input = 0
	}
	billableInput := input
	if cacheInInput {
		billableInput = input - cacheRead - cacheCreation
		if billableInput < 0 {
			billableInput = 0
		}
	}
	output := tokens.Output
	if reasoningInOutput {
		output += tokens.Reasoning
	}
	if output < 0 {
		output = 0
	}

	usd := (float64(billableInput)*rate.Input +
		float64(cacheRead)*rate.CacheRead +
		float64(cacheCreation)*rate.CacheWrite +
		float64(output)*rate.Output) / 1_000_000
	return int64(math.Round(usd * 1e9)), true
}

func promptTokens(tokens Tokens, cacheInInput bool) int64 {
	input := tokens.Input
	if input < 0 {
		input = 0
	}
	cacheRead := tokens.CacheRead
	if cacheRead == 0 {
		cacheRead = tokens.Cached
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	cacheCreation := tokens.CacheCreation
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	if cacheInInput {
		if sum := cacheRead + cacheCreation; input < sum {
			return sum
		}
		return input
	}
	return input + cacheRead + cacheCreation
}
