package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// windowLabels are the three limit windows the OpenCode Go usage endpoint
// reports today, in the order the page shows them.
var windowOrder = []string{"rolling", "weekly", "monthly"}

var windowLabels = map[string]string{
	"rolling": "5 小时",
	"weekly":  "本周",
	"monthly": "本月",
}

// Window is one limit window of one key. Percent is the share the upstream
// reports as used; RemainingPercent is its complement and exists because the
// page reads better when it can show "还剩 x%".
type Window struct {
	Key              string    `json:"key"`
	Label            string    `json:"label"`
	Status           string    `json:"status,omitempty"`
	Percent          float64   `json:"percent"`
	RemainingPercent float64   `json:"remaining_percent"`
	ResetsAt         time.Time `json:"resets_at,omitempty"`
}

// Snapshot is the quota state of one credential.
type Snapshot struct {
	ID        string    `json:"id"`
	Masked    string    `json:"masked"`
	Egress    string    `json:"egress"`
	GroupKeys []string  `json:"groups"`
	Windows   []Window  `json:"windows,omitempty"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// FetchOptions carries what one probe run needs.
type FetchOptions struct {
	// UsageURL is the canonical usage endpoint.
	UsageURL string
	// Timeout bounds one probe.
	Timeout time.Duration
	// Session, when set, is sent as x-opencode-session.
	Session string
	// Concurrency bounds parallel probes; the key pool is small and the
	// endpoint is cheap, but a 30-key pool should still not fan out fully.
	Concurrency int
}

type usageEnvelope struct {
	Usage map[string]usageWindow `json:"usage"`
}

type usageWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

// Fetch queries the usage endpoint for every credential. Individual failures
// are reported per credential rather than failing the whole call, so one dead
// key never blanks the page.
func Fetch(ctx context.Context, credentials []Credential, options FetchOptions) []Snapshot {
	out := make([]Snapshot, len(credentials))
	if options.Concurrency <= 0 {
		options.Concurrency = 4
	}
	const maxConcurrency = 8
	if options.Concurrency > maxConcurrency {
		options.Concurrency = maxConcurrency
	}
	if options.Timeout <= 0 {
		options.Timeout = 20 * time.Second
	}

	semaphore := make(chan struct{}, options.Concurrency)
	done := make(chan int, len(credentials))
	launched := 0
	for index, credential := range credentials {
		snapshot := Snapshot{
			ID:        credential.ID,
			Masked:    credential.Masked,
			Egress:    displayEgress(credential.Egress),
			GroupKeys: append([]string(nil), credential.GroupKeys...),
			FetchedAt: time.Now().UTC(),
		}
		out[index] = snapshot
		if strings.TrimSpace(credential.Key) == "" {
			out[index].Error = "Key 内容为空"
			continue
		}
		launched++
		go func(index int, credential Credential) {
			semaphore <- struct{}{}
			defer func() { <-semaphore; done <- index }()
			windows, errFetch := fetchUsage(ctx, credential, options)
			if errFetch != nil {
				out[index].Error = errFetch.Error()
				out[index].FetchedAt = time.Now().UTC()
				return
			}
			out[index].Windows = windows
			out[index].FetchedAt = time.Now().UTC()
		}(index, credential)
	}
	// Drain every launched worker before returning: the snapshots it writes
	// must not be handed to the caller while a goroutine still owns an index.
	for range launched {
		<-done
	}
	return out
}

// fetchUsage queries the endpoint for one key.
func fetchUsage(ctx context.Context, credential Credential, options FetchOptions) ([]Window, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, options.UsageURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("构建用量请求失败: %w", errRequest)
	}
	request.Header.Set("Authorization", "Bearer "+credential.Key)
	request.Header.Set("Accept", "application/json")
	if options.Session != "" {
		request.Header.Set("x-opencode-session", options.Session)
	}

	client := &http.Client{Timeout: options.Timeout, Transport: transportFor(credential.Egress)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, fmt.Errorf("连接用量接口失败: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()

	body, errRead := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if errRead != nil {
		return nil, fmt.Errorf("读取用量接口响应失败: %w", errRead)
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("凭据无效（HTTP %d）", response.StatusCode)
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, fmt.Errorf("用量接口返回 HTTP %d: %s", response.StatusCode, snippet(body))
	}

	var decoded usageEnvelope
	if errUnmarshal := json.Unmarshal(body, &decoded); errUnmarshal != nil {
		return nil, fmt.Errorf("解析用量接口响应失败: %w", errUnmarshal)
	}
	if len(decoded.Usage) == 0 {
		return nil, fmt.Errorf("用量接口未返回窗口数据")
	}

	windows := make([]Window, 0, len(decoded.Usage))
	appendWindow := func(name string, value usageWindow) {
		window := Window{
			Key:              name,
			Label:            windowLabels[name],
			Status:           value.Status,
			Percent:          clampPercent(value.Percent),
			RemainingPercent: clampPercent(100 - value.Percent),
		}
		if window.Label == "" {
			window.Label = name
		}
		if value.ResetsAt != "" {
			if parsed, errParse := time.Parse(time.RFC3339, value.ResetsAt); errParse == nil {
				window.ResetsAt = parsed.UTC()
			}
		}
		windows = append(windows, window)
	}
	seen := make(map[string]struct{}, len(decoded.Usage))
	for _, name := range windowOrder {
		value, okWindow := decoded.Usage[name]
		if !okWindow {
			continue
		}
		seen[name] = struct{}{}
		appendWindow(name, value)
	}
	// Unknown windows are reported after the known ones instead of being
	// dropped, so an upstream addition shows up without a plugin change.
	extra := make([]string, 0)
	for name := range decoded.Usage {
		if _, known := seen[name]; known {
			continue
		}
		extra = append(extra, name)
	}
	sortStrings(extra)
	for _, name := range extra {
		appendWindow(name, decoded.Usage[name])
	}
	return windows, nil
}

// transportFor returns the shared transport for one egress decision. Probes
// reuse transports so a page refresh does not open a new connection pool per
// key.
var (
	transportMu sync.Mutex
	transports  = map[string]*http.Transport{}
)

func transportFor(proxy string) *http.Transport {
	transportMu.Lock()
	defer transportMu.Unlock()
	if existing, ok := transports[proxy]; ok {
		return existing
	}
	transport := &http.Transport{
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
	}
	if proxy != "" {
		if parsed, errParse := url.Parse(proxy); errParse == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	transports[proxy] = transport
	return transport
}

// displayEgress renders the egress decision for the page: "direct" or the
// proxy URL itself.
func displayEgress(egress string) string {
	if strings.TrimSpace(egress) == "" {
		return "direct"
	}
	return egress
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		return text[:200]
	}
	return text
}

// sortStrings is a tiny insertion sort: the unknown-window list is at most a
// handful of entries and avoiding the sort import keeps the query path honest.
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
