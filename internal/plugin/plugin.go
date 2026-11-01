// Package plugin implements the CLIProxyAPI plugin RPC surface: registration,
// model catalogue publication, model routing, request execution, and the
// management page.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/catalog"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/paths"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/pricing"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/reqlog"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/stats"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/upstream"
)

// menuLabel is the sidebar entry shown under the 插件 group. Only the main
// build registers a management page, so the product appears once in the
// sidebar even though four protocol faces are loaded.
const menuLabel = brand.Name

// Runtime is the process-wide plugin state.
type Runtime struct {
	role      roles.Role
	root      string
	client    *upstream.Client
	settings  atomic.Pointer[config.Settings]
	resolver  atomic.Pointer[catalog.Resolver]
	hostProxy atomic.Pointer[string]
	// hostAuth is the host auth directory learned from the model registration
	// summary; only the main face writes into it.
	hostAuth atomic.Pointer[string]

	// accounts is the background projection of the configured keys onto host
	// auth files. It is a separate struct so the sync goroutine never takes
	// Runtime.mu.
	accounts accountSyncState

	buildVersion string
	debugMode    atomic.Bool
	debugPath    string

	mu            sync.Mutex
	pluginID      string
	configRaw     []byte
	sharedPath    string
	sharedStamp   time.Time
	lastError     string
	migrationNote string
	startedAt     time.Time

	// stats is lazily opened because sibling faces only need it when they
	// finish their first stream.
	statsMu sync.Mutex
	stats   *stats.Store

	// reqLog is lazily opened for the same reason as stats: only the face that
	// executes a request writes one.
	reqLogMu sync.Mutex
	reqLog   *reqlog.Store

	// modelList is the diff from the latest successful /models fetch against
	// the embedded snapshot. /api/models refreshes it; /api/stats only reads
	// it, so /api/stats never makes an upstream call of its own.
	modelListMu sync.Mutex
	modelList   pricing.ModelListCheck
}

// New builds the runtime for one role.
func New(role roles.Role) *Runtime {
	root := paths.ResolveRoot()
	runtime := &Runtime{
		role:      role,
		root:      root,
		client:    upstream.New(config.Default()),
		startedAt: time.Now(),
	}
	runtime.sharedPath = config.Default().StatePath(root)
	return runtime
}

// Debug reports whether diagnostics are written to the state directory.
func (r *Runtime) Debug() bool { return r.debugMode.Load() }

// Version reports the build version.
func (r *Runtime) Version() string {
	if r.buildVersion == "" {
		return "dev"
	}
	return r.buildVersion
}

// SetVersion records the build version stamped by the linker.
func (r *Runtime) SetVersion(value string) {
	r.buildVersion = strings.TrimSpace(value)
}

// Role reports the build role.
func (r *Runtime) Role() roles.Role { return r.role }

// Resolver returns the model id resolver for the active configuration.
func (r *Runtime) Resolver() *catalog.Resolver {
	if current := r.resolver.Load(); current != nil {
		return current
	}
	return catalog.New(r.Settings())
}

// Settings returns the active settings. The host publishes its upstream proxy
// through the model registration summary, which arrives after the first
// register, so it is overlaid on every read.
func (r *Runtime) Settings() config.Settings {
	settings := config.Default()
	if current := r.settings.Load(); current != nil {
		settings = *current
	}
	if settings.HostProxyURL == "" {
		settings.HostProxyURL = r.hostProxyURL()
	}
	return settings
}

// Dispatch handles one plugin RPC.
func (r *Runtime) Dispatch(method string, request []byte) []byte {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return r.handleRegister(method, request)
	case pluginabi.MethodPluginShutdown:
		return r.handleShutdown()
	case pluginabi.MethodPluginQuiesce:
		r.markQuiesced()
		return okEmpty()
	case pluginabi.MethodExecutorIdentifier:
		return okJSON(identifierResponse{Identifier: r.role.Provider})
	case pluginabi.MethodExecutorExecute:
		return r.handleExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return r.handleExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return abi.Fail("unsupported", "OpenCode Go 插件不支持 token 计数", http.StatusNotImplemented)
	case pluginabi.MethodExecutorHTTPRequest:
		return abi.Fail("unsupported", "OpenCode Go 插件不支持 HTTP 桥接", http.StatusNotImplemented)
	case pluginabi.MethodModelRegister, pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return r.handleStaticModels(request)
	case pluginabi.MethodModelRoute:
		return r.handleModelRoute(request)
	case pluginabi.MethodManagementRegister:
		return r.handleManagementRegister(request)
	case pluginabi.MethodManagementHandle:
		return r.handleManagementHandle(request)
	case pluginabi.MethodUsageHandle:
		return r.handleUsage(request)
	default:
		return abi.Fail("unknown_method", "unknown method: "+method, http.StatusNotFound)
	}
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registrationCapabilities struct {
	ModelProvider         bool     `json:"model_provider"`
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool     `json:"management_api"`
	UsagePlugin           bool     `json:"usage_plugin,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type modelRouteResponse struct {
	Handled     bool   `json:"Handled"`
	TargetKind  string `json:"TargetKind,omitempty"`
	Target      string `json:"Target,omitempty"`
	TargetModel string `json:"TargetModel,omitempty"`
	Reason      string `json:"Reason,omitempty"`
}

func (r *Runtime) handleRegister(method string, request []byte) []byte {
	var lifecycle lifecycleRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
			return abi.Fail("invalid_config", fmt.Sprintf("解析插件配置失败: %v", errUnmarshal), http.StatusBadRequest)
		}
	}

	settings, errParse := config.Parse(lifecycle.ConfigYAML)

	r.mu.Lock()
	r.configRaw = append([]byte(nil), lifecycle.ConfigYAML...)
	if r.sharedPath == "" {
		r.sharedPath = config.Default().StatePath(r.root)
	}
	sharedPath := r.sharedPath
	isMain := r.role.Name == roles.Chat
	r.mu.Unlock()

	if isMain {
		// The chat-role build owns the configuration and publishes the shared
		// runtime snapshot every sibling reads.
		if errParse != nil {
			r.setLastError(errParse.Error())
		} else {
			r.setLastError("")
			r.migrateLegacyState(settings)
			if errWrite := config.WriteSharedState(sharedPath, config.SharedState{
				Version:   r.Version(),
				WrittenAt: time.Now().UTC(),
				Settings:  settings,
			}); errWrite != nil {
				r.setLastError("写入共享配置失败: " + errWrite.Error())
			}
		}
	} else {
		// Sibling builds prefer the shared snapshot and fall back to their own
		// configuration block when it is not published yet.
		if shared, errLoad := config.ReadSharedState(sharedPath); errLoad == nil {
			settings = shared.Settings
			if errParse != nil {
				settings = shared.Settings
			}
			r.setLastError("")
		} else if errParse != nil {
			// Without a snapshot a parse failure is fatal for this instance.
			return abi.Fail("invalid_config", errParse.Error(), http.StatusBadRequest)
		} else {
			r.setLastError("尚未读取到主插件发布的共享配置（cpa-opencodego）")
		}
		r.mu.Lock()
		r.sharedStamp = stampOf(sharedPath)
		r.mu.Unlock()
	}

	r.applySettings(settings)
	if r.role.Name == roles.Chat {
		// A reconfigure can change the key list, so re-project the accounts.
		// The sync runs in the background: register must not touch the auth
		// directory while the host is still assembling its runtime state.
		r.resetAccountSyncState()
		r.syncAccountsAsync()
	}
	_ = method
	return r.registrationPayload()
}

func (r *Runtime) registrationPayload() []byte {
	// All four faces publish the same name, logo and repository, so the four
	// rows in the management centre's plugin list read as one product: the main
	// build carries the product name, the siblings append their protocol face.
	// Only the main build declares the configuration fields, because the
	// siblings share the snapshot it publishes and their own config block holds
	// nothing but the enable flag.
	metadata := pluginapi.Metadata{
		Name:             r.role.Label,
		Version:          r.Version(),
		Author:           brand.Author,
		GitHubRepository: brand.Repository,
		Logo:             brand.Logo,
		ConfigFields:     configFields(r.role),
	}
	capabilities := registrationCapabilities{
		ModelProvider:         true,
		Executor:              true,
		ExecutorModelScope:    string(pluginapi.ExecutorModelScopeBoth),
		ExecutorInputFormats:  r.role.Formats,
		ExecutorOutputFormats: r.role.Formats,
	}
	if r.role.Name == roles.Chat {
		capabilities.ManagementAPI = true
		// The main face receives the host's usage events for every request the
		// four faces execute. The siblings only measure their own stream
		// timing; registering usage on all four would count one record four
		// times if the host ever broadcast it.
		capabilities.UsagePlugin = true
	}
	raw, errMarshal := abi.OK(registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      metadata,
		Capabilities:  capabilities,
	})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

func (r *Runtime) handleShutdown() []byte {
	r.Shutdown()
	return okEmpty()
}

// Shutdown flushes the statistics store, closes the request log, and removes
// the auth files the main face projected. A hot replacement is announced with
// plugin.quiesce first, in which case the successor owns the files and the
// purge is skipped.
func (r *Runtime) Shutdown() {
	r.closeAndPurge()
	r.statsMu.Lock()
	store := r.stats
	r.statsMu.Unlock()
	if store != nil {
		store.Shutdown()
	}
	r.reqLogMu.Lock()
	reqLog := r.reqLog
	r.reqLogMu.Unlock()
	if reqLog != nil {
		reqLog.Shutdown()
	}
}

func (r *Runtime) applySettings(settings config.Settings) {
	settings.HostProxyURL = r.hostProxyURL()
	settings.BuildVersion = r.Version()
	copied := settings
	r.settings.Store(&copied)
	r.resolver.Store(catalog.New(copied))
	r.client.Update(copied)
	r.debugMode.Store(settings.Debug)
	r.mu.Lock()
	r.debugPath = filepath.Join(settings.StateRoot(r.root), "debug.log")
	r.mu.Unlock()
}

func (r *Runtime) setLastError(message string) {
	r.mu.Lock()
	r.lastError = message
	r.mu.Unlock()
}

// LastError reports the most recent configuration problem.
func (r *Runtime) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastError
}

// refreshShared reloads the shared snapshot for sibling builds when the main
// plugin republished it. The check is an os.Stat, so it is cheap enough to run
// per request.
func (r *Runtime) refreshShared() {
	if r.role.Name == roles.Chat {
		return
	}
	r.mu.Lock()
	path := r.sharedPath
	stamp := r.sharedStamp
	r.mu.Unlock()
	if path == "" {
		return
	}
	current := stampOf(path)
	if current.IsZero() || current.Equal(stamp) {
		return
	}
	shared, errLoad := config.ReadSharedState(path)
	if errLoad != nil {
		return
	}
	r.mu.Lock()
	r.sharedStamp = current
	r.mu.Unlock()
	r.applySettings(shared.Settings)
	r.setLastError("")
}

func stampOf(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// hostProxyURL returns the upstream proxy configured on the host, which is used
// when a model inherits proxy behaviour and the plugin has no proxy-url.
func (r *Runtime) hostProxyURL() string {
	if current := r.hostProxy.Load(); current != nil {
		return *current
	}
	return ""
}

// models returns the catalogue entries this protocol face serves, in the shape
// the host registers them.
func (r *Runtime) models() []pluginapi.ModelInfo {
	resolver := r.Resolver()
	entries := resolver.EntriesForFamily(r.role.Name)
	out := make([]pluginapi.ModelInfo, 0, len(entries))
	for _, entry := range entries {
		// ID is the id clients call. A model renamed by an alias is published
		// under that alias alone, and Name has to follow it: the host prints
		// Name in the Gemini model list, so pointing it at the upstream id
		// would expose the id the alias replaced.
		out = append(out, pluginapi.ModelInfo{
			ID:                         entry.ID,
			Object:                     "model",
			Created:                    r.startedAt.Unix(),
			OwnedBy:                    "opencode",
			DisplayName:                entry.DisplayName,
			Name:                       entry.ID,
			Description:                entry.Note,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return out
}

func (r *Runtime) handleStaticModels(request []byte) []byte {
	r.refreshShared()
	if len(request) > 0 {
		var staticRequest pluginapi.StaticModelRequest
		if errUnmarshal := json.Unmarshal(request, &staticRequest); errUnmarshal == nil {
			proxyChanged := r.rememberHostSummary(staticRequest.Host)
			if proxyChanged {
				// Push the learned proxy into the upstream client's settings so
				// inherited proxy models stop going out direct.
				r.applySettings(r.Settings())
			}
			// The host publishes its auth directory in this summary, so this is
			// where the account projection can start.
			r.syncAccountsAsync()
		}
	}
	raw, errMarshal := abi.OK(pluginapi.ModelResponse{
		Provider: r.role.Provider,
		Models:   r.models(),
	})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

// handleModelRoute declines every model on purpose.
//
// The router used to claim this build's enabled catalogue so the host would
// force the request through the plugin provider. That decision outweighs the
// host's own arbitration, which prefers a native executor when another provider
// also serves the same model id: once the operator configures a native
// fallback channel for an OpenCode Go model (for example a paid channel for
// gemini-4-argon), the host skips this provider's executor at registration and
// a forced route dies with
//
//	auth_not_found: no auth available (providers=opencode-go-gemini, model=gemini-4-argon)
//
// even though the keys have quota and the request is valid. Declining the route
// lets the host registry decide: models only this provider serves still resolve
// to it (the plugin registers both the model and its executor), while models the
// host serves natively fall back to the native executor instead of failing.
func (r *Runtime) handleModelRoute(request []byte) []byte {
	r.refreshShared()
	if len(request) > 0 {
		var routeRequest pluginapi.ModelRouteRequest
		if errUnmarshal := json.Unmarshal(request, &routeRequest); errUnmarshal != nil {
			return abi.Fail("invalid_request", errUnmarshal.Error(), http.StatusBadRequest)
		}
	}
	raw, errMarshal := abi.OK(modelRouteResponse{Handled: false})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

func okJSON(v any) []byte {
	raw, errMarshal := abi.OK(v)
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

func okEmpty() []byte {
	raw, errMarshal := abi.OK(map[string]any{})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}
