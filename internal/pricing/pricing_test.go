package pricing

import (
	"strings"
	"testing"
	"time"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/models"
)

func nanoDollars(value float64) int64 { return int64(value*1e9 + 0.5) }

func TestPlanNormalizationAndLabels(t *testing.T) {
	cases := map[string]Plan{
		"": PlanGo, "go": PlanGo, "GO": PlanGo, "go-plus": PlanGoPlus,
		"go_plus": PlanGoPlus, "goplus": PlanGoPlus, "plus": PlanGoPlus, "weird": PlanGo,
	}
	for input, want := range cases {
		if got := NormalizePlan(input); got != want {
			t.Errorf("NormalizePlan(%q) = %q, want %q", input, got, want)
		}
	}
	if PlanGo.Label() != "Go" || PlanGoPlus.Label() != "Go Plus" {
		t.Error("unexpected plan labels")
	}
}

func TestCostChatAndCacheConvention(t *testing.T) {
	offPeak := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) // Saturday
	peak := time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)     // Monday, 02:00 UTC
	between := time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC)  // Monday, off-peak
	sundayPeakHour := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)

	tokens := Tokens{Input: 1_000_000, Output: 1_000_000}
	for _, tc := range []struct {
		name string
		at   time.Time
		want float64
	}{
		{"weekend", offPeak, 0.75},
		{"weekday peak", peak, 1.50},
		{"weekday between windows", between, 0.75},
		{"sunday peak hour", sundayPeakHour, 0.75},
	} {
		got, priced := Cost("deepseek-v4.1-flash", tokens, true, false, tc.at)
		if !priced {
			t.Fatalf("%s: model must be priced", tc.name)
		}
		if got != nanoDollars(tc.want) {
			t.Errorf("%s: cost = %d nanoUSD, want %d", tc.name, got, nanoDollars(tc.want))
		}
	}

	// chat input is inclusive of cached tokens: only the uncached part pays the
	// input rate, while cache reads and writes pay their own rates.
	cached := Tokens{Input: 250_000, Output: 100_000, CacheRead: 100_000, CacheCreation: 50_000}
	got, _ := Cost("qwen3.7-plus", cached, true, false, offPeak)
	want := 100_000*0.40/1e6 + 100_000*0.04/1e6 + 50_000*0.50/1e6 + 100_000*1.60/1e6
	if got != nanoDollars(want) {
		t.Errorf("chat cached cost = %d, want %d", got, nanoDollars(want))
	}

	// A message record can never drive the billable input negative.
	overCached := Tokens{Input: 1_000, CacheRead: 100_000, CacheCreation: 1_000, Output: 1_000}
	got, _ = Cost("qwen3.8-max", overCached, true, false, offPeak)
	want = 100_000*0.25/1e6 + 1_000*2.50/1e6 + 1_000*6.00/1e6
	if got != nanoDollars(want) {
		t.Errorf("over-cached chat cost = %d, want %d", got, nanoDollars(want))
	}
}

func TestCostMessagesExcludesCacheFromInput(t *testing.T) {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	tokens := Tokens{Input: 1_000, CacheRead: 100_000, CacheCreation: 1_000, Output: 1_000}
	got, priced := Cost("claude-haiku-5-5", tokens, false, false, at)
	if !priced {
		t.Fatal("Claude Haiku must be priced")
	}
	// Prompt = 102K > 100K, so the higher tier applies.
	want := 1_000*0.50/1e6 + 100_000*0.05/1e6 + 1_000*0.625/1e6 + 1_000*2.50/1e6
	if got != nanoDollars(want) {
		t.Errorf("messages cost = %d, want %d", got, nanoDollars(want))
	}
}

func TestPromptTierBoundary(t *testing.T) {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		prompt int64
		want   float64
	}{
		{256_000, 256_000 * 0.40 / 1e6},
		{256_001, 256_001 * 1.20 / 1e6},
	}
	for _, tc := range cases {
		got, _ := Cost("qwen3.7-plus", Tokens{Input: tc.prompt}, true, false, at)
		if got != nanoDollars(tc.want) {
			t.Errorf("prompt %d: cost = %d, want %d", tc.prompt, got, nanoDollars(tc.want))
		}
	}
}

func TestCostGeminiIncludesReasoningInOutput(t *testing.T) {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	tokens := Tokens{Output: 1_000, Reasoning: 1_000}
	got, _ := Cost("gemini-4-argon", tokens, true, true, at)
	if want := nanoDollars(2_000 * 20.0 / 1e6); got != want {
		t.Errorf("gemini cost = %d, want %d", got, want)
	}
	without, _ := Cost("gemini-4-argon", tokens, true, false, at)
	if without >= got {
		t.Errorf("reasoning must add to Gemini output cost: with=%d without=%d", got, without)
	}
}

func TestFreeAndUnknownModels(t *testing.T) {
	at := time.Now()
	for _, model := range []string{"longcat-2.5-preview-free", "beam-free", "cat-jam-free"} {
		if got, priced := Cost(model, Tokens{Input: 1_000_000, Output: 1_000_000}, true, false, at); !priced || got != 0 {
			t.Errorf("%s: cost=%d priced=%v, want free and priced", model, got, priced)
		}
		if _, unlimited, known := Limit(model, PlanGo); !known || !unlimited {
			t.Errorf("%s: limit known=%v unlimited=%v, want unlimited", model, known, unlimited)
		}
		if !IsFree(model) {
			t.Errorf("%s must be free", model)
		}
	}
	if Has("omen-alpha") || IsFree("omen-alpha") {
		t.Error("omen-alpha has no published Go price and must stay unpriced")
	}
	if _, priced := Cost("omen-alpha", Tokens{Input: 1}, true, false, at); priced {
		t.Error("unpriced models must report priced=false")
	}
	if _, _, known := Limit("omen-alpha", PlanGo); known {
		t.Error("unpriced models have no monthly limit")
	}
}

func TestMonthlyLimitsByPlan(t *testing.T) {
	cases := []struct {
		model     string
		plan      Plan
		wantLimit float64
		unlimited bool
		known     bool
	}{
		{"qwen3.8-max", PlanGo, 15, false, true},
		{"qwen3.8-max", PlanGoPlus, 60, false, true},
		{"deepseek-v4.1-flash", PlanGoPlus, 160, false, true},
		{"gemini-4-argon", PlanGoPlus, 240, false, true},
		{"step-5-preview", PlanGo, 60, false, true},
		{"longcat-2.5-preview-free", PlanGoPlus, 0, true, true},
		{"glm-5.1", PlanGo, 0, false, false},
		{"not-a-model", PlanGo, 0, false, false},
	}
	for _, tc := range cases {
		limit, unlimited, known := Limit(tc.model, tc.plan)
		if limit != tc.wantLimit || unlimited != tc.unlimited || known != tc.known {
			t.Errorf("Limit(%q, %q) = (%v, %v, %v), want (%v, %v, %v)",
				tc.model, tc.plan, limit, unlimited, known, tc.wantLimit, tc.unlimited, tc.known)
		}
	}
}

func TestSnapshotFreshnessUsesCalendarDays(t *testing.T) {
	if RefreshAfterDays != 30 {
		t.Fatalf("RefreshAfterDays = %d, want 30", RefreshAfterDays)
	}
	cases := []struct {
		now     time.Time
		wantAge int
		wantDue bool
	}{
		{time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC), 0, false},
		{time.Date(2026, 10, 17, 23, 59, 59, 999999999, time.UTC), 0, false},
		{time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), 1, false},
		{time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), 29, false},
		{time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 30, true},
		{time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC), 75, true},
	}
	for _, tc := range cases {
		got := SnapshotFreshness(tc.now)
		if got.AgeDays != tc.wantAge || got.UpdateDue != tc.wantDue {
			t.Errorf("SnapshotFreshness(%s) = age %d due %v, want %d/%v",
				tc.now.Format(time.RFC3339), got.AgeDays, got.UpdateDue, tc.wantAge, tc.wantDue)
		}
		if got.CheckedAt.Format("2006-01-02") != SnapshotCheckedAt {
			t.Errorf("checked date = %s, want %s", got.CheckedAt.Format("2006-01-02"), SnapshotCheckedAt)
		}
		if got.DueAt.Format("2006-01-02") != "2026-11-16" {
			t.Errorf("due date = %s, want 2026-11-16", got.DueAt.Format("2006-01-02"))
		}
	}
}

func TestSnapshotModelIDsSortedAndUnique(t *testing.T) {
	ids := SnapshotModelIDs()
	if len(ids) != 37 {
		t.Fatalf("snapshot model count = %d, want 37", len(ids))
	}
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		if id == "" || id != strings.TrimSpace(id) {
			t.Errorf("snapshot id %q is not clean", id)
		}
		if index > 0 && ids[index-1] >= id {
			t.Errorf("snapshot ids are not sorted/unique at %q then %q", ids[index-1], id)
		}
		if _, exists := seen[id]; exists {
			t.Errorf("duplicate snapshot id %q", id)
		}
		seen[id] = struct{}{}
	}
	ids[0] = "mutated"
	if SnapshotModelIDs()[0] == "mutated" {
		t.Error("SnapshotModelIDs must return a copy")
	}
}

func TestCompareModelList(t *testing.T) {
	at := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	same := append(SnapshotModelIDs(), SnapshotModelIDs()[0], "  ")
	if got := CompareModelList(same, at); got.Changed() || !got.CheckedAt.Equal(at) {
		t.Errorf("unchanged list = %+v", got)
	}
	if got := CompareModelList(nil, at); got.Changed() || !got.CheckedAt.IsZero() {
		t.Errorf("empty list must not look like every model was removed: %+v", got)
	}

	current := SnapshotModelIDs()
	removed := current[0]
	current = append(current[1:], "future-model", "future-model", " ")
	got := CompareModelList(current, at)
	if !got.CheckedAt.Equal(at) || !got.Changed() {
		t.Fatalf("changed list = %+v", got)
	}
	if want := []string{"future-model"}; len(got.Added) != 1 || got.Added[0] != want[0] {
		t.Errorf("added = %v, want %v", got.Added, want)
	}
	if want := []string{removed}; len(got.Removed) != 1 || got.Removed[0] != want[0] {
		t.Errorf("removed = %v, want %v", got.Removed, want)
	}
}

// The catalogue is the plugin's advertised model list; every id that is not
// explicitly unpriced must have a price in the book, otherwise the statistics
// tab would silently report "未定价" for a model the plugin serves.
func TestCatalogueHasAPrice(t *testing.T) {
	for _, model := range models.Known() {
		if model.ID == "omen-alpha" {
			if Has(model.ID) {
				t.Errorf("%s must stay unpriced", model.ID)
			}
			continue
		}
		if !Has(model.ID) {
			t.Errorf("catalogue model %s has no price entry", model.ID)
		}
	}
}
