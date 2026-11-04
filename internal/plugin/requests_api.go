package plugin

import (
	"errors"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/pricing"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/reqlog"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

// Request entry defaults and bounds.
const (
	requestsDefaultLimit = 200
	requestsMaxLimit     = 1000
)

// RequestEntry is one row of the 请求日志 tab. Timings and token counters are
// measured by the plugin while it proxies the upstream response.
type RequestEntry struct {
	ID            string    `json:"id"`
	At            time.Time `json:"at"`
	Role          string    `json:"role"`
	Model         string    `json:"model"`
	DisplayName   string    `json:"display_name"`
	ClientIDs     []string  `json:"client_ids,omitempty"`
	Requested     string    `json:"requested,omitempty"`
	UpstreamModel string    `json:"upstream_model,omitempty"`
	Key           string    `json:"key,omitempty"`
	Stream        bool      `json:"stream"`
	Status        int       `json:"status,omitempty"`
	Failed        bool      `json:"failed"`
	Canceled      bool      `json:"canceled,omitempty"`
	Error         string    `json:"error,omitempty"`
	LogID         string    `json:"log_id,omitempty"`
	Session       string    `json:"session,omitempty"`

	TTFTms       float64 `json:"ttft_ms,omitempty"`
	Generationms float64 `json:"generation_ms,omitempty"`
	Totalms      float64 `json:"total_ms"`

	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	ReasoningTokens     int64    `json:"reasoning_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	TokensSeen          bool     `json:"tokens_seen"`
	GenTPS              float64  `json:"gen_tps"`
	E2ETPS              float64  `json:"e2e_tps"`
	CostUSD             float64  `json:"cost_usd,omitempty"`
	Repairs             []string `json:"repairs,omitempty"`
	Finish              string   `json:"finish,omitempty"`
	Incomplete          bool     `json:"incomplete,omitempty"`
}

// RequestTotals aggregates the rows returned by one query.
type RequestTotals struct {
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed"`
	Canceled        int64   `json:"canceled"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	TTFTSamples     int64   `json:"ttft_samples"`
	AvgTTFTMS       float64 `json:"avg_ttft_ms"`
	GenTPSSamples   int64   `json:"gen_tps_samples"`
	AvgGenTPS       float64 `json:"avg_gen_tps"`
	E2ETPSSamples   int64   `json:"e2e_tps_samples"`
	AvgE2ETPS       float64 `json:"avg_e2e_tps"`
	UpstreamSuccess int64   `json:"upstream_success"`
}

// RequestsPayload is GET /api/requests.
type RequestsPayload struct {
	GeneratedAt    time.Time      `json:"generated_at"`
	Enabled        bool           `json:"enabled"`
	Limit          int            `json:"limit"`
	RetentionHours int            `json:"retention_hours"`
	Entries        []RequestEntry `json:"entries"`
	Totals         RequestTotals  `json:"totals"`
	Warnings       []string       `json:"warnings,omitempty"`
}

// requestsPayload builds the request-log read model. It never contacts the
// upstream and never returns a prompt, a response body, or a key secret.
func (r *Runtime) requestsPayload(query url.Values) RequestsPayload {
	now := time.Now().UTC()
	enabled := r.Settings().RequestLogEnabled()
	limit := parseRequestsLimit(query.Get("limit"))
	payload := RequestsPayload{
		GeneratedAt:    now,
		Enabled:        enabled,
		Limit:          limit,
		RetentionHours: int(reqlog.Retention / time.Hour),
		Entries:        []RequestEntry{},
	}
	var warnings []string
	addWarning := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		for _, existing := range warnings {
			if existing == message {
				return
			}
		}
		warnings = append(warnings, message)
	}
	if !enabled {
		addWarning("请求日志已在设置中关闭；打开 request-log 后新请求才会被记录。")
		payload.Totals = RequestTotals{}
		payload.Warnings = warnings
		return payload
	}

	rows := make([]reqlog.Entry, 0, 256)
	if store := r.reqLogStore(); store != nil {
		rows = append(rows, store.Snapshot()...)
		if message := store.LastError(); message != "" {
			addWarning(message)
		}
	}
	for _, sibling := range r.siblingReqLogFiles() {
		entries, errRead := reqlog.ReadFile(sibling.Path, string(sibling.Role.Name))
		if errRead != nil {
			if !errors.Is(errRead, os.ErrNotExist) {
				addWarning(sibling.Path + ": " + errRead.Error())
			}
			continue
		}
		rows = append(rows, entries...)
	}

	family := strings.ToLower(strings.TrimSpace(query.Get("family")))
	key := strings.TrimSpace(query.Get("key"))
	modelFilter := strings.TrimSpace(query.Get("model"))
	failedOnly := truthy(query.Get("failed"))
	since := time.Time{}
	if raw := strings.TrimSpace(query.Get("since")); raw != "" {
		if parsed, errParse := time.Parse(time.RFC3339Nano, raw); errParse == nil {
			since = parsed.UTC()
		}
	}

	resolver := r.Resolver()
	filtered := make([]RequestEntry, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		row = normalizeCanceledEntry(row)
		if row.ID != "" {
			if _, duplicate := seen[row.ID]; duplicate {
				continue
			}
			seen[row.ID] = struct{}{}
		}
		if family != "" && !strings.EqualFold(row.Role, family) {
			continue
		}
		if since != (time.Time{}) && row.At.Before(since) {
			continue
		}
		if failedOnly && !row.Failed() {
			continue
		}
		display, rowFamily, clientIDs, _ := statsModelInfo(resolver, row.Model)
		if display == "" {
			display = row.Model
		}
		if rowFamily == "" {
			rowFamily = row.Role
		}
		if key != "" && row.Key != key {
			continue
		}
		if modelFilter != "" && !requestMatchesModel(row, modelFilter, clientIDs) {
			continue
		}
		filtered = append(filtered, r.requestEntry(row, display, clientIDs, rowFamily))
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if !filtered[i].At.Equal(filtered[j].At) {
			return filtered[i].At.After(filtered[j].At)
		}
		return filtered[i].ID > filtered[j].ID
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	payload.Entries = filtered
	payload.Totals = requestTotals(filtered)
	payload.Warnings = warnings
	return payload
}

// requestEntry converts one stored row into the page's shape, resolving the
// display name and computing the throughput numbers in one place.
func (r *Runtime) requestEntry(row reqlog.Entry, display string, clientIDs []string, family string) RequestEntry {
	entry := RequestEntry{
		ID:                  row.ID,
		At:                  row.At,
		Role:                row.Role,
		Model:               row.Model,
		DisplayName:         display,
		ClientIDs:           clientIDs,
		Requested:           row.Requested,
		UpstreamModel:       row.UpstreamModel,
		Key:                 row.Key,
		Stream:              row.Stream,
		Status:              row.Status,
		Failed:              row.Failed(),
		Canceled:            row.Canceled,
		Error:               row.Error,
		LogID:               row.LogID,
		Session:             row.Session,
		TTFTms:              roundFloat(row.TTFTms, 1),
		Generationms:        roundFloat(row.Generationms, 1),
		Totalms:             roundFloat(row.Totalms, 1),
		InputTokens:         row.Input,
		OutputTokens:        row.Output,
		ReasoningTokens:     row.Reasoning,
		CacheReadTokens:     row.CacheRead,
		CacheCreationTokens: row.CacheCreation,
		TotalTokens:         row.TotalTokens,
		TokensSeen:          row.TokensSeen,
		Repairs:             row.Repairs,
		Finish:              row.Finish,
		Incomplete:          row.Incomplete,
	}
	effective := row.Output
	if roles.Name(row.Role) == roles.Gemini {
		effective += row.Reasoning
	}
	if !entry.Failed && !entry.Canceled && entry.TokensSeen && effective > 0 {
		if row.Generationms > 0 {
			entry.GenTPS = roundFloat(float64(effective)/(row.Generationms/1000), 2)
		}
		if row.Totalms > 0 {
			entry.E2ETPS = roundFloat(float64(effective)/(row.Totalms/1000), 2)
		}
	}
	if entry.TokensSeen && row.Model != "" {
		cost, priced := pricing.Cost(row.Model, pricing.Tokens{
			Input:         row.Input,
			Output:        row.Output,
			Reasoning:     row.Reasoning,
			Cached:        row.CacheRead,
			CacheRead:     row.CacheRead,
			CacheCreation: row.CacheCreation,
		}, roles.Name(row.Role) != roles.Messages, roles.Name(row.Role) == roles.Gemini, row.At)
		if priced {
			entry.CostUSD = roundFloat(float64(cost)/1e9, 9)
		}
	}
	return entry
}

// normalizeCanceledEntry repairs rows written before the plugin learned to
// separate a downstream cancel from an upstream failure. The host closes the
// stream bridge when the client disconnects (or when the plugin instance is
// unloaded), yet those rows were stored as finish=error with the host callback
// message. The request log keeps seven days, so this read-time shim makes the
// existing rows display correctly without rewriting the file; new rows carry
// canceled=true directly.
func normalizeCanceledEntry(entry reqlog.Entry) reqlog.Entry {
	if entry.Canceled || !entry.Stream || entry.Status < 200 || entry.Status >= 300 || entry.Finish != "error" {
		return entry
	}
	if !downstreamClosedMessage(entry.Error) {
		return entry
	}
	entry.Canceled = true
	entry.Finish = "canceled"
	entry.Error = ""
	return entry
}

// requestMatchesModel accepts the configured model id, the upstream id, or any
// client-facing alias.
func requestMatchesModel(row reqlog.Entry, filter string, clientIDs []string) bool {
	if strings.EqualFold(row.Model, filter) || strings.EqualFold(row.UpstreamModel, filter) ||
		strings.EqualFold(row.Requested, filter) {
		return true
	}
	for _, id := range clientIDs {
		if strings.EqualFold(id, filter) {
			return true
		}
	}
	return false
}

// requestTotals aggregates the rows that are actually returned to the page.
func requestTotals(entries []RequestEntry) RequestTotals {
	var totals RequestTotals
	var ttftSum, genTPSSum, e2eTPSSum float64
	for _, entry := range entries {
		totals.Requests++
		if entry.Failed {
			totals.Failed++
		} else if entry.Canceled {
			totals.Canceled++
		} else if entry.Status >= 200 && entry.Status < 300 {
			totals.UpstreamSuccess++
		}
		totals.InputTokens += entry.InputTokens
		totals.OutputTokens += entry.OutputTokens
		totals.TotalTokens += entry.TotalTokens
		if entry.TTFTms > 0 {
			totals.TTFTSamples++
			ttftSum += entry.TTFTms
		}
		if entry.GenTPS > 0 {
			totals.GenTPSSamples++
			genTPSSum += entry.GenTPS
		}
		if entry.E2ETPS > 0 {
			totals.E2ETPSSamples++
			e2eTPSSum += entry.E2ETPS
		}
	}
	if totals.TTFTSamples > 0 {
		totals.AvgTTFTMS = roundFloat(ttftSum/float64(totals.TTFTSamples), 1)
	}
	if totals.GenTPSSamples > 0 {
		totals.AvgGenTPS = roundFloat(genTPSSum/float64(totals.GenTPSSamples), 2)
	}
	if totals.E2ETPSSamples > 0 {
		totals.AvgE2ETPS = roundFloat(e2eTPSSum/float64(totals.E2ETPSSamples), 2)
	}
	return totals
}

func parseRequestsLimit(raw string) int {
	limit, errAtoi := strconv.Atoi(strings.TrimSpace(raw))
	if errAtoi != nil || limit <= 0 {
		return requestsDefaultLimit
	}
	if limit > requestsMaxLimit {
		return requestsMaxLimit
	}
	return limit
}

func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
