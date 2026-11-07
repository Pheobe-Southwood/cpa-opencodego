// Package plugin implements the read-only quota viewer: registration, the
// management page that lists the host's OpenCode Go keys and the per-key usage
// probe. It registers no executor, no model and no host callback that can change
// routing, so loading it cannot alter how CLIProxyAPI serves requests.
package plugin

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
)

// Runtime is the process-wide plugin state.
type Runtime struct {
	mu        sync.Mutex
	version   string
	pluginID  string
	config    Config
	configErr string
	source    *hostSource
	startedAt time.Time

	cacheMu sync.Mutex
	cache   quotaCache
}

// quotaCache is the last page payload, reused for cache-seconds. It is keyed by
// the host config file stamp so editing config.yaml invalidates it even when the
// cache has not expired.
type quotaCache struct {
	stamp   string
	payload []byte
	at      time.Time
}

// New builds the runtime with the default (empty) configuration.
func New() *Runtime {
	config := defaultConfig()
	return &Runtime{
		config:    config,
		source:    newHostSource(config),
		startedAt: time.Now(),
	}
}

// Version reports the build version stamped by the linker.
func (r *Runtime) Version() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.version == "" {
		return "dev"
	}
	return r.version
}

// SetVersion records the build version stamped by the linker.
func (r *Runtime) SetVersion(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = value
}

// Dispatch routes one host RPC call.
func (r *Runtime) Dispatch(method string, request []byte) []byte {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return r.handleRegister(method, request)
	case pluginabi.MethodPluginShutdown:
		r.Shutdown()
		return okEmpty()
	case pluginabi.MethodPluginQuiesce:
		return okEmpty()
	case pluginabi.MethodManagementRegister:
		return r.handleManagementRegister(request)
	case pluginabi.MethodManagementHandle:
		return r.handleManagementHandle(request)
	default:
		return abi.Fail("unknown_method", "unknown method: "+method, http.StatusNotFound)
	}
}

// Shutdown drops idle upstream connections. The plugin owns no files, so there
// is nothing else to release.
func (r *Runtime) Shutdown() {
	transportMu.Lock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
	transports = map[string]*http.Transport{}
	transportMu.Unlock()
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// handleRegister parses the plugin's own configuration block. A bad block does
// not fail the registration: the page shows the parse error instead, because a
// quota viewer that cannot be opened is harder to fix than one that explains
// what is wrong.
func (r *Runtime) handleRegister(method string, request []byte) []byte {
	var lifecycle lifecycleRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
			return abi.Fail("invalid_config", "解析插件配置失败: "+errUnmarshal.Error(), http.StatusBadRequest)
		}
	}
	config, errConfig := parseConfig(lifecycle.ConfigYAML)

	r.mu.Lock()
	r.config = config
	r.source = newHostSource(config)
	if errConfig != nil {
		r.configErr = errConfig.Error()
	} else {
		r.configErr = ""
	}
	r.mu.Unlock()

	r.cacheMu.Lock()
	r.cache = quotaCache{}
	r.cacheMu.Unlock()

	_ = method
	return r.registrationPayload()
}

// Config returns the active configuration.
func (r *Runtime) Config() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config
}

// configError reports a bad plugin configuration block, if any.
func (r *Runtime) configError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configErr
}

// sourceRef returns the loader for the active configuration.
func (r *Runtime) sourceRef() *hostSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source
}

// registrationCapabilities is the JSON shape the host decodes into a capability
// set. The SDK's Capabilities struct holds interfaces and cannot be marshalled,
// so the flags are spelled out here.
type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

// registrationPayload advertises a Management-API-only plugin.
func (r *Runtime) registrationPayload() []byte {
	metadata := pluginapi.Metadata{
		Name:             brand.Name,
		Version:          r.Version(),
		Author:           brand.Author,
		GitHubRepository: brand.Repository,
		Logo:             brand.Logo,
		ConfigFields:     configFields(),
	}
	raw, errMarshal := abi.OK(registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      metadata,
		Capabilities:  registrationCapabilities{ManagementAPI: true},
	})
	if errMarshal != nil {
		return abi.Fail("marshal_error", errMarshal.Error(), http.StatusInternalServerError)
	}
	return raw
}

// configFields describes the optional configuration block for the management
// centre's plugin form.
func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{
			Name:        "config-path",
			Type:        pluginapi.ConfigFieldTypeString,
			Default:     "",
			Description: "宿主 config.yaml 的路径。留空则从宿主进程的 -config 参数自动发现。",
		},
		{
			Name:        "egress",
			Type:        pluginapi.ConfigFieldTypeEnum,
			Default:     egressAuto,
			EnumValues:  []string{egressAuto, egressDirect, egressProxy},
			Description: "额度探测的出口：auto 跟随分组直连/代理，direct 直连，proxy 走 proxy-url。",
		},
		{
			Name:        "proxy-url",
			Type:        pluginapi.ConfigFieldTypeString,
			Default:     "",
			Description: "egress=proxy 时使用的代理；留空则用宿主 requests.proxy-url。",
		},
		{
			Name:        "hosts",
			Type:        pluginapi.ConfigFieldTypeArray,
			Default:     []string{"opencode.ai"},
			Description: "视为 OpenCode Go 的 base-url 主机名列表。",
		},
		{
			Name:        "usage-url",
			Type:        pluginapi.ConfigFieldTypeString,
			Default:     "",
			Description: "覆盖用量接口地址；留空则由 base-url 推导为 …/inference/go/v1/usage。",
		},
		{
			Name:        "session",
			Type:        pluginapi.ConfigFieldTypeString,
			Default:     "",
			Description: "探测时附带的 x-opencode-session 头（可选）。",
		},
		{
			Name:        "timeout-seconds",
			Type:        pluginapi.ConfigFieldTypeInteger,
			Default:     defaultTimeoutSeconds,
			Description: "单个 /usage 探测的超时（秒）。",
		},
		{
			Name:        "cache-seconds",
			Type:        pluginapi.ConfigFieldTypeInteger,
			Default:     defaultCacheSeconds,
			Description: "额度快照的缓存时间（秒），页面上的刷新按钮会绕过它。",
		},
	}
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
