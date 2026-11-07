package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/dashboard"
)

// Resource paths served under /v0/resource/plugins/<pluginID>/. Only /dashboard
// carries a Menu, so the management centre shows exactly one sidebar entry for
// this plugin.
const (
	resourceDashboard = "/dashboard"
	resourceQuota     = "/api/quota"
)

// managementRoute is the JSON shape the host decodes into
// pluginapi.ManagementRoute / pluginapi.ResourceRoute. The SDK types carry
// handler interfaces and cannot be marshalled, so the route is spelled out.
type managementRoute struct {
	Method      string
	Path        string
	Menu        string
	Description string
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []managementRoute `json:"resources,omitempty"`
}

type managementRegistrationRequest struct {
	Plugin           pluginapi.Metadata
	BasePath         string
	ResourceBasePath string
}

func (r *Runtime) handleManagementRegister(request []byte) []byte {
	if len(request) > 0 {
		var registerRequest managementRegistrationRequest
		if errUnmarshal := json.Unmarshal(request, &registerRequest); errUnmarshal == nil {
			if pluginID := lastPathSegment(registerRequest.ResourceBasePath); pluginID != "" {
				r.mu.Lock()
				r.pluginID = pluginID
				r.mu.Unlock()
			}
		}
	}
	response := managementRegistrationResponse{
		Resources: []managementRoute{
			{
				Method:      http.MethodGet,
				Path:        resourceDashboard,
				Menu:        brand.Name,
				Description: "OpenCode Go 每个 Key 的额度余量（只读）",
			},
			{
				Method:      http.MethodGet,
				Path:        resourceQuota,
				Description: "OpenCode Go 各 Key 的额度快照（只读 JSON）",
			},
		},
	}
	return okJSON(response)
}

// managementRequest mirrors pluginapi.ManagementRequest. The SDK type has no
// JSON tags, so the fields travel with their Go names.
type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

func (r *Runtime) handleManagementHandle(request []byte) []byte {
	var management managementRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &management); errUnmarshal != nil {
			return abi.Fail("invalid_request", "解析管理请求失败: "+errUnmarshal.Error(), http.StatusBadRequest)
		}
	}
	path := strings.TrimSpace(management.Path)
	switch {
	case strings.HasSuffix(path, resourceDashboard):
		return okJSON(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    htmlHeaders(),
			Body:       dashboard.Page(r.PluginID(), r.Version()),
		})
	case strings.HasSuffix(path, resourceQuota):
		force := strings.TrimSpace(management.Query.Get("refresh")) == "1"
		payload, status := r.quotaPayload(managementContext(management), force)
		return okJSON(pluginapi.ManagementResponse{
			StatusCode: status,
			Headers:    jsonHeaders(),
			Body:       payload,
		})
	default:
		return okJSON(pluginapi.ManagementResponse{
			StatusCode: http.StatusNotFound,
			Headers:    jsonHeaders(),
			Body:       []byte(`{"error":"not_found"}`),
		})
	}
}

// managementContext keeps a cancelled browser request from leaving six probes
// running against the upstream.
func managementContext(request managementRequest) context.Context {
	_ = request
	return context.Background()
}

// quotaPayload builds the page's read model: the host config projection plus one
// usage snapshot per unique key.
func (r *Runtime) quotaPayload(ctx context.Context, force bool) ([]byte, int) {
	config := r.Config()
	payload := quotaPayloadModel{
		GeneratedAt: time.Now().UTC(),
		ConfigError: r.configError(),
		GlobalProxy: config.ProxyURL,
	}
	if payload.ConfigError != "" {
		return mustJSON(payload), http.StatusOK
	}

	source := r.sourceRef()
	hostConfig, errLoad := source.Load(force)
	if errLoad != nil {
		payload.ConfigError = errLoad.Error()
		return mustJSON(payload), http.StatusOK
	}

	payload.ConfigPath = hostConfig.Path
	payload.GlobalProxy = displayEgressOrGlobal(hostConfig.GlobalProxy)
	payload.UsageURL = hostConfig.UsageURL
	payload.Warnings = append(payload.Warnings, hostConfig.Warnings...)
	payload.Groups = hostConfig.Groups

	cacheKey := fmt.Sprintf("%s|%s", hostConfig.Path, hostConfig.LoadedAt.UTC().Format(time.RFC3339Nano))
	if !force && config.Cache() > 0 {
		r.cacheMu.Lock()
		cached := r.cache
		r.cacheMu.Unlock()
		if cached.payload != nil && cached.stamp == cacheKey && time.Since(cached.at) < config.Cache() {
			return cached.payload, http.StatusOK
		}
	}

	options := FetchOptions{
		UsageURL:    hostConfig.UsageURL,
		Timeout:     config.Timeout(),
		Session:     config.Session,
		Concurrency: 4,
	}
	snapshots := Fetch(ctx, hostConfig.Credentials, options)
	if payload.UsageURL == "" {
		for index := range snapshots {
			if snapshots[index].Error == "" {
				snapshots[index].Error = "未找到可用的用量接口地址"
			}
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].Masked < snapshots[j].Masked })
	payload.Keys = snapshots
	payload.Summary = summarize(hostConfig, snapshots)

	raw := mustJSON(payload)
	if config.Cache() > 0 {
		r.cacheMu.Lock()
		r.cache = quotaCache{stamp: cacheKey, payload: raw, at: time.Now()}
		r.cacheMu.Unlock()
	}
	return raw, http.StatusOK
}

// quotaPayloadModel is the /api/quota document.
type quotaPayloadModel struct {
	GeneratedAt time.Time    `json:"generated_at"`
	ConfigPath  string       `json:"config_path,omitempty"`
	ConfigError string       `json:"config_error,omitempty"`
	GlobalProxy string       `json:"global_proxy,omitempty"`
	UsageURL    string       `json:"usage_url,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
	Summary     quotaSummary `json:"summary"`
	Groups      []HostGroup  `json:"groups,omitempty"`
	Keys        []Snapshot   `json:"keys"`
}

type quotaSummary struct {
	Keys       int     `json:"keys"`
	OK         int     `json:"ok"`
	Failed     int     `json:"failed"`
	Groups     int     `json:"groups"`
	MaxRolling float64 `json:"max_rolling_percent"`
}

func summarize(hostConfig *HostConfig, snapshots []Snapshot) quotaSummary {
	summary := quotaSummary{Keys: len(snapshots), Groups: len(hostConfig.Groups)}
	for _, snapshot := range snapshots {
		if snapshot.Error != "" {
			summary.Failed++
			continue
		}
		summary.OK++
		for _, window := range snapshot.Windows {
			if window.Key == "rolling" && window.Percent > summary.MaxRolling {
				summary.MaxRolling = window.Percent
			}
		}
	}
	return summary
}

func displayEgressOrGlobal(global string) string {
	if strings.TrimSpace(global) == "" {
		return ""
	}
	return global
}

// PluginID reports the id the host assigned, learned from the resource base path
// during management.register.
func (r *Runtime) PluginID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pluginID != "" {
		return r.pluginID
	}
	return "cpa-opencodego-quota"
}

func lastPathSegment(path string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(path), "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

func htmlHeaders() http.Header {
	return http.Header{
		"Content-Type":            []string{"text/html; charset=utf-8"},
		"Cache-Control":           []string{"no-store"},
		"Referrer-Policy":         []string{"no-referrer"},
		"X-Content-Type-Options":  []string{"nosniff"},
		"Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data: blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
	}
}

func jsonHeaders() http.Header {
	return http.Header{
		"Content-Type":           []string{"application/json; charset=utf-8"},
		"Cache-Control":          []string{"no-store"},
		"X-Content-Type-Options": []string{"nosniff"},
	}
}

func mustJSON(value any) []byte {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return []byte(`{"error":"marshal_error"}`)
	}
	return raw
}
