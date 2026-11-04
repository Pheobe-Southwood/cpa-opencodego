package plugin

import (
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/catalog"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/pricing"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/stats"
)

// StatsCounts is the token/cost/timing part of one /api/stats row. The same
// shape is used per model and for the totals, so the page can format one way.
type StatsCounts struct {
	Requests          int64   `json:"requests"`
	FailedRequests    int64   `json:"failed_requests"`
	CanceledRequests  int64   `json:"canceled_requests"`
	StreamRequests    int64   `json:"stream_requests"`
	UnmeteredRequests int64   `json:"unmetered_requests"`
	PricedRequests    int64   `json:"priced_requests"`
	UnpricedRequests  int64   `json:"unpriced_requests"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	ReasoningTokens   int64   `json:"reasoning_tokens"`
	CachedTokens      int64   `json:"cached_tokens"`
	CacheReadTokens   int64   `json:"cache_read_tokens"`
	CacheCreation     int64   `json:"cache_creation_tokens"`
	TotalTokens       int64   `json:"total_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	TTFTSamples       int64   `json:"ttft_samples"`
	TPSSamples        int64   `json:"tps_samples"`
	AvgTPS            float64 `json:"avg_tps"`
	GenTPSSamples     int64   `json:"gen_tps_samples"`
	AvgGenTPS         float64 `json:"avg_gen_tps"`
	AvgTTFTMS         float64 `json:"avg_ttft_ms"`
}

// StatsKeyModelQuota is one key's usage and remaining quota on one model.
type StatsKeyModelQuota struct {
	KeyName          string   `json:"key_name"`
	CostUSD          float64  `json:"cost_usd"`
	RemainingUSD     float64  `json:"remaining_usd"`
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
}

// StatsKeySummary describes one credential available for stats filtering.
type StatsKeySummary struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
}

// StatsModel is one used model in the 统计 tab.
type StatsModel struct {
	ID              string               `json:"id"`
	DisplayName     string               `json:"display_name"`
	ClientIDs       []string             `json:"client_ids"`
	Family          string               `json:"family,omitempty"`
	Configured      bool                 `json:"configured"`
	Priced          bool                 `json:"priced"`
	Free            bool                 `json:"free"`
	MonthlyLimitUSD float64              `json:"monthly_limit_usd"`
	MonthlyPercent  *float64             `json:"monthly_percent"`
	FirstUsed       time.Time            `json:"first_used,omitempty"`
	LastUsed        time.Time            `json:"last_used,omitempty"`
	ByKey           []StatsKeyModelQuota `json:"by_key,omitempty"`
	StatsCounts
}

// StatsTotals is the footer/summary of the same read model.
type StatsTotals struct {
	ModelsUsed int64 `json:"models_used"`
	StatsCounts
}

// StatsRange describes the window the caller asked for. Since is empty for
// range=all. SinceSource is "quota" when the page supplied the monthly reset,
// "calendar" when the server fell back to the UTC month, and empty for all.
type StatsRange struct {
	Kind        string `json:"kind"`
	Since       string `json:"since,omitempty"`
	Until       string `json:"until"`
	SinceSource string `json:"since_source,omitempty"`
	Clamped     bool   `json:"clamped"`
}

// StatsUsageRecords is the usage-event diagnostic: how many host records the
// main face received and how many matched one of the four faces.
type StatsUsageRecords struct {
	Received int64 `json:"received"`
	Matched  int64 `json:"matched"`
}

// StatsPayload is GET /api/stats.
type StatsPayload struct {
	GeneratedAt               time.Time         `json:"generated_at"`
	Range                     StatsRange        `json:"range"`
	Plan                      string            `json:"plan"`
	PlanLabel                 string            `json:"plan_label"`
	PriceBookRevision         string            `json:"price_book_revision"`
	PriceBookURL              string            `json:"price_book_url"`
	PriceBookCheckedAt        string            `json:"price_book_checked_at"`
	PriceBookAgeDays          int               `json:"price_book_age_days"`
	PriceBookRefreshAfterDays int               `json:"price_book_refresh_after_days"`
	PriceBookUpdateDue        bool              `json:"price_book_update_due"`
	PriceBookDueAt            string            `json:"price_book_due_at"`
	ModelListCheckedAt        string            `json:"model_list_checked_at"`
	ModelListChanged          bool              `json:"model_list_changed"`
	ModelListAdded            []string          `json:"model_list_added,omitempty"`
	ModelListRemoved          []string          `json:"model_list_removed,omitempty"`
	UsageRecords              StatsUsageRecords `json:"usage_records"`
	Keys                      []StatsKeySummary `json:"keys"`
	SelectedKey               string            `json:"selected_key,omitempty"`
	Models                    []StatsModel      `json:"models"`
	Totals                    StatsTotals       `json:"totals"`
	Warnings                  []string          `json:"warnings,omitempty"`
}

// statsPayload builds the page's read model. It never contacts the upstream and
// never includes a key, a prompt, or a response body.
func (r *Runtime) statsPayload(query url.Values) StatsPayload {
	now := time.Now().UTC()
	freshness := pricing.SnapshotFreshness(now)
	modelList := r.modelListStatus()
	modelListCheckedAt := ""
	if !modelList.CheckedAt.IsZero() {
		modelListCheckedAt = modelList.CheckedAt.UTC().Format(time.RFC3339Nano)
	}
	kind, since, source, clamped := resolveStatsRange(query, now)
	store := r.statsStore()
	snapshot := store.Snapshot(since)
	models := make(map[string]stats.ModelSnapshot, len(snapshot.Models))
	for id, model := range snapshot.Models {
		models[id] = model
	}

	warnings := make([]string, 0, 4)
	warningSeen := map[string]struct{}{}
	addWarning := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		if _, exists := warningSeen[message]; exists {
			return
		}
		warningSeen[message] = struct{}{}
		warnings = append(warnings, message)
	}
	if message := r.statsLoadError(); message != "" {
		addWarning(message)
	}
	if message := snapshot.LastError; message != "" {
		addWarning(message)
	}

	for _, sibling := range r.siblingStatsFiles() {
		file, errRead := stats.ReadFile(sibling.Path, string(sibling.Role.Name))
		if errRead != nil {
			if !errors.Is(errRead, os.ErrNotExist) {
				addWarning(filepath.Base(sibling.Path) + ": " + errRead.Error())
			}
			continue
		}
		for id, persisted := range file.Models {
			if persisted == nil {
				continue
			}
			timing := stats.RangeCounter(persisted, since)
			entry, exists := models[id]
			if !exists {
				entry = stats.ModelSnapshot{
					Model:     id,
					Family:    persisted.Family,
					FirstUsed: persisted.FirstUsed,
					LastUsed:  persisted.LastUsed,
					Keys:      map[string]stats.KeySnapshot{},
				}
			}
			if entry.Keys == nil {
				entry.Keys = map[string]stats.KeySnapshot{}
			}
			entry.Counter.MergeTiming(timing)
			if entry.Family == "" {
				entry.Family = persisted.Family
			}
			if !persisted.FirstUsed.IsZero() && (entry.FirstUsed.IsZero() || persisted.FirstUsed.Before(entry.FirstUsed)) {
				entry.FirstUsed = persisted.FirstUsed
			}
			if !persisted.LastUsed.IsZero() && persisted.LastUsed.After(entry.LastUsed) {
				entry.LastUsed = persisted.LastUsed
			}
			for kid, kpersisted := range persisted.Keys {
				if kpersisted == nil {
					continue
				}
				ktiming := stats.RangeKeyCounter(kpersisted, since)
				kentry, kexists := entry.Keys[kid]
				if !kexists {
					kentry = stats.KeySnapshot{
						FirstUsed: kpersisted.FirstUsed,
						LastUsed:  kpersisted.LastUsed,
					}
				}
				kentry.Counter.MergeTiming(ktiming)
				if !kpersisted.FirstUsed.IsZero() && (kentry.FirstUsed.IsZero() || kpersisted.FirstUsed.Before(kentry.FirstUsed)) {
					kentry.FirstUsed = kpersisted.FirstUsed
				}
				if !kpersisted.LastUsed.IsZero() && kpersisted.LastUsed.After(kentry.LastUsed) {
					kentry.LastUsed = kpersisted.LastUsed
				}
				entry.Keys[kid] = kentry
			}
			models[id] = entry
		}
	}

	if snapshot.Received > 0 && snapshot.Matched == 0 {
		addWarning("收到的宿主用量事件没有匹配到本插件的四个协议面；请确认宿主版本支持 usage 插件，或在调试日志中查看 provider/source 标识。")
	}

	settings := r.Settings()
	plan := pricing.NormalizePlan(settings.Plan)
	resolver := r.Resolver()

	selectedKey := strings.TrimSpace(query.Get("key"))
	allKeysMap := map[string]StatsKeySummary{}
	for _, k := range settings.Keys {
		if strings.TrimSpace(k.Name) != "" {
			allKeysMap[k.Name] = StatsKeySummary{
				Name:     k.Name,
				Enabled:  k.EnabledOrDefault(),
				Priority: k.Priority,
			}
		}
	}
	for _, m := range models {
		for kName := range m.Keys {
			if strings.TrimSpace(kName) != "" {
				if _, ok := allKeysMap[kName]; !ok {
					allKeysMap[kName] = StatsKeySummary{
						Name:     kName,
						Enabled:  false,
						Priority: 0,
					}
				}
			}
		}
	}
	keysList := make([]StatsKeySummary, 0, len(allKeysMap))
	for _, ks := range allKeysMap {
		keysList = append(keysList, ks)
	}
	sort.Slice(keysList, func(i, j int) bool {
		if keysList[i].Priority != keysList[j].Priority {
			return keysList[i].Priority > keysList[j].Priority
		}
		return keysList[i].Name < keysList[j].Name
	})

	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rows := make([]StatsModel, 0, len(ids))
	var totalCounter stats.Counter
	for _, id := range ids {
		entry := models[id]
		var effectiveCounter stats.Counter
		var firstUsed, lastUsed time.Time
		if selectedKey != "" {
			ks, ok := entry.Keys[selectedKey]
			if !ok || ks.Counter.Empty() {
				// Fallback for legacy historical stats records recorded before per-key attribution:
				// if entry has no keys recorded at all and there is only 1 configured key which matches selectedKey.
				if len(entry.Keys) == 0 && len(settings.Keys) == 1 && settings.Keys[0].Name == selectedKey {
					effectiveCounter = entry.Counter
					firstUsed = entry.FirstUsed
					lastUsed = entry.LastUsed
				} else {
					continue
				}
			} else {
				effectiveCounter = ks.Counter
				firstUsed = ks.FirstUsed
				lastUsed = ks.LastUsed
			}
		} else {
			effectiveCounter = entry.Counter
			firstUsed = entry.FirstUsed
			lastUsed = entry.LastUsed
		}

		display, family, clientIDs, configured := statsModelInfo(resolver, id)
		if family == "" {
			family = entry.Family
		}
		if display == "" {
			display = id
		}
		free := pricing.IsFree(id)
		priced := pricing.Has(id)
		limitUSD, unlimited, known := pricing.Limit(id, plan)
		if !known || unlimited {
			limitUSD = 0
		}

		var percent *float64
		if kind == "month" && priced && !free && limitUSD > 0 {
			value := (float64(effectiveCounter.CostNanoUSD) / 1e9) / limitUSD * 100
			percent = &value
		}

		var byKey []StatsKeyModelQuota
		if len(keysList) > 0 {
			byKey = make([]StatsKeyModelQuota, 0, len(keysList))
			for _, k := range keysList {
				kName := k.Name
				var kCostNano int64
				if ks, ok := entry.Keys[kName]; ok {
					kCostNano = ks.Counter.CostNanoUSD
				} else if len(entry.Keys) == 0 && len(settings.Keys) == 1 && settings.Keys[0].Name == kName {
					kCostNano = entry.Counter.CostNanoUSD
				}
				kCostUSD := roundFloat(float64(kCostNano)/1e9, 4)
				var kRemUSD float64
				var kUsedPct *float64
				var kRemPct *float64
				if priced && !free && limitUSD > 0 {
					kRemUSD = roundFloat(math.Max(0, limitUSD-kCostUSD), 4)
					uVal := roundFloat((kCostUSD/limitUSD)*100, 1)
					rVal := roundFloat(math.Max(0, 100-uVal), 1)
					kUsedPct = &uVal
					kRemPct = &rVal
				}
				byKey = append(byKey, StatsKeyModelQuota{
					KeyName:          kName,
					CostUSD:          kCostUSD,
					RemainingUSD:     kRemUSD,
					UsedPercent:      kUsedPct,
					RemainingPercent: kRemPct,
				})
			}
		}

		totalCounter.Add(effectiveCounter)
		counts := statsCountsFrom(effectiveCounter)
		rows = append(rows, StatsModel{
			ID:              id,
			DisplayName:     display,
			ClientIDs:       clientIDs,
			Family:          family,
			Configured:      configured,
			Priced:          priced,
			Free:            free,
			MonthlyLimitUSD: limitUSD,
			MonthlyPercent:  percent,
			FirstUsed:       firstUsed,
			LastUsed:        lastUsed,
			ByKey:           byKey,
			StatsCounts:     counts,
		})
	}

	totals := statsCountsFrom(totalCounter)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CostUSD != rows[j].CostUSD {
			return rows[i].CostUSD > rows[j].CostUSD
		}
		if rows[i].TotalTokens != rows[j].TotalTokens {
			return rows[i].TotalTokens > rows[j].TotalTokens
		}
		return rows[i].ID < rows[j].ID
	})

	rangePayload := StatsRange{
		Kind:        kind,
		Until:       now.Format(time.RFC3339),
		SinceSource: source,
		Clamped:     clamped,
	}
	if !since.IsZero() {
		rangePayload.Since = since.Format(time.RFC3339)
	}

	return StatsPayload{
		GeneratedAt:               now,
		Range:                     rangePayload,
		Plan:                      string(plan),
		PlanLabel:                 plan.Label(),
		PriceBookRevision:         pricing.Revision,
		PriceBookURL:              pricing.SourceURL,
		PriceBookCheckedAt:        freshness.CheckedAt.Format("2006-01-02"),
		PriceBookAgeDays:          freshness.AgeDays,
		PriceBookRefreshAfterDays: pricing.RefreshAfterDays,
		PriceBookUpdateDue:        freshness.UpdateDue,
		PriceBookDueAt:            freshness.DueAt.Format("2006-01-02"),
		ModelListCheckedAt:        modelListCheckedAt,
		ModelListChanged:          modelList.Changed(),
		ModelListAdded:            modelList.Added,
		ModelListRemoved:          modelList.Removed,
		UsageRecords: StatsUsageRecords{
			Received: snapshot.Received,
			Matched:  snapshot.Matched,
		},
		Keys:        keysList,
		SelectedKey: selectedKey,
		Models:      rows,
		Totals:      StatsTotals{ModelsUsed: int64(len(rows)), StatsCounts: totals},
		Warnings:    warnings,
	}
}

// recordModelListCheck stores the latest upstream /models diff. Only a
// non-empty, successfully fetched list is recorded: an error or empty response
// must not look like every model was retired.
func (r *Runtime) recordModelListCheck(ids []string, checkedAt time.Time) {
	check := pricing.CompareModelList(ids, checkedAt)
	if check.CheckedAt.IsZero() {
		return
	}
	r.modelListMu.Lock()
	r.modelList = check
	r.modelListMu.Unlock()
}

// modelListStatus returns a copy so the payload cannot race with a refresh.
func (r *Runtime) modelListStatus() pricing.ModelListCheck {
	r.modelListMu.Lock()
	defer r.modelListMu.Unlock()
	check := r.modelList
	check.Added = append([]string(nil), check.Added...)
	check.Removed = append([]string(nil), check.Removed...)
	return check
}

// resolveStatsRange turns the query into the window. The page computes the
// monthly window from the latest quota reset minus one month; when it cannot,
// the server falls back to the UTC calendar month and says so.
func resolveStatsRange(query url.Values, now time.Time) (kind string, since time.Time, source string, clamped bool) {
	kind = strings.ToLower(strings.TrimSpace(query.Get("range")))
	if kind != "all" {
		kind = "month"
	}
	if kind == "all" {
		return kind, time.Time{}, "", false
	}
	if raw := strings.TrimSpace(query.Get("since")); raw != "" {
		if parsed, errParse := time.Parse(time.RFC3339Nano, raw); errParse == nil {
			since = parsed.UTC()
			source = "quota"
		}
	}
	if since.IsZero() {
		year, month, _ := now.Date()
		since = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		source = "calendar"
	}
	cutoff := now.Add(-stats.Retention)
	if since.Before(cutoff) {
		since = cutoff
		clamped = true
	}
	if since.After(now) {
		since = now
		clamped = true
	}
	return kind, since, source, clamped
}

func statsModelInfo(resolver *catalog.Resolver, id string) (display, family string, clientIDs []string, configured bool) {
	display = id
	var aliases []string
	for _, entry := range resolver.Entries() {
		if entry.BaseID != id {
			continue
		}
		configured = true
		if entry.DisplayName != "" {
			display = entry.DisplayName
		}
		if family == "" {
			family = string(entry.Family)
		}
		if entry.ID != id && !containsString(aliases, entry.ID) {
			aliases = append(aliases, entry.ID)
		}
	}
	if len(aliases) > 0 {
		clientIDs = aliases
	} else {
		clientIDs = []string{id}
	}
	return display, family, clientIDs, configured
}

func statsCountsFrom(counter stats.Counter) StatsCounts {
	return StatsCounts{
		Requests:          counter.Requests,
		FailedRequests:    counter.Failed,
		CanceledRequests:  counter.Canceled,
		StreamRequests:    counter.Streams,
		UnmeteredRequests: counter.Unmetered,
		PricedRequests:    counter.Priced,
		UnpricedRequests:  counter.Unpriced,
		InputTokens:       counter.Input,
		OutputTokens:      counter.Output,
		ReasoningTokens:   counter.Reasoning,
		CachedTokens:      counter.Cached,
		CacheReadTokens:   counter.CacheRead,
		CacheCreation:     counter.CacheCreation,
		TotalTokens:       counter.Total,
		CostUSD:           roundFloat(float64(counter.CostNanoUSD)/1e9, 9),
		TTFTSamples:       counter.TTFTSamples,
		TPSSamples:        counter.LatencySamples,
		AvgTPS:            avgTPS(counter),
		GenTPSSamples:     counter.GenSamples,
		AvgGenTPS:         avgGenTPS(counter),
		AvgTTFTMS:         avgTTFT(counter),
	}
}

// avgTPS is successful-request output tokens divided by the host-reported
// wall-clock request latency. Numerator and denominator are accumulated from
// the same usage records; it is an end-to-end throughput number, not a
// decode-only rate.
func avgTPS(counter stats.Counter) float64 {
	if counter.LatencySamples == 0 || counter.LatencyNano <= 0 || counter.LatencyOutput <= 0 {
		return 0
	}
	return roundFloat(float64(counter.LatencyOutput)/(float64(counter.LatencyNano)/1e9), 2)
}

// avgGenTPS is the plugin-measured decode rate: streamed output tokens divided
// by the time from first content to last content. It excludes TTFT by
// construction. The host usage event has no request id that can be joined to
// the timing sample, so this is a secondary estimate; the paired end-to-end TPS
// above remains the primary number. Gemini reasoning is not separately
// available in this historical numerator, so Gemini can be slightly low.
func avgGenTPS(counter stats.Counter) float64 {
	if counter.GenSamples == 0 || counter.GenerationNano <= 0 || counter.StreamOutput <= 0 {
		return 0
	}
	// Streams is available in legacy stats files and covers every streaming
	// request. It can include failures or zero-output streams, so using it as
	// the coverage denominator is deliberately conservative. New stats also
	// record StreamOutputSamples, but mixing legacy and new counters must not
	// shrink the denominator to the new-only sample count.
	outputSamples := counter.Streams
	if counter.StreamOutputSamples > outputSamples {
		outputSamples = counter.StreamOutputSamples
	}
	if outputSamples == 0 {
		return 0
	}
	output := float64(counter.StreamOutput)
	if counter.GenSamples < outputSamples {
		// Do not add output tokens for streams that have no generation sample:
		// that was the old metric's inflation path. Scale the numerator by the
		// timing coverage instead. The UI still shows the sample count, and the
		// paired overall TPS remains the authoritative number.
		output *= float64(counter.GenSamples) / float64(outputSamples)
	}
	return roundFloat(output/(float64(counter.GenerationNano)/1e9), 2)
}

func avgTTFT(counter stats.Counter) float64 {
	if counter.TTFTSamples == 0 {
		return 0
	}
	return roundFloat(float64(counter.TTFTNano)/1e6/float64(counter.TTFTSamples), 1)
}

func roundFloat(value float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}
