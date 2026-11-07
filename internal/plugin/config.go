package plugin

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Egress modes accepted by the egress field. The plugin only reads them: it
// never routes host traffic, it only decides which way its own /usage probes
// leave the machine.
const (
	egressAuto   = "auto"
	egressDirect = "direct"
	egressProxy  = "proxy"
)

const (
	defaultTimeoutSeconds = 20
	defaultCacheSeconds   = 10
	maxTimeoutSeconds     = 120
	maxCacheSeconds       = 600
)

// Config is the plugin configuration under plugins.configs.<pluginID>. Every
// field is optional: with no configuration at all the plugin discovers the host
// configuration from the host process argv and reports every key whose base URL
// points at one of the hosts below.
type Config struct {
	// ConfigPath pins the host configuration file. Empty means "discover it":
	// the "-config" argument of the host process, then CPA_CONFIG_PATH, then
	// the container's default locations.
	ConfigPath string `yaml:"config-path"`
	// Hosts limits which base URLs count as OpenCode Go. Defaults to
	// "opencode.ai"; a group whose base URL host equals one of these or ends in
	// ".<host>" is in scope.
	Hosts []string `yaml:"hosts"`
	// Egress decides how the /usage probes leave the machine: auto (default,
	// follow the same direct/proxy split as the host group), direct (never use
	// a proxy) or proxy (always use proxy-url / the host's global proxy).
	Egress string `yaml:"egress"`
	// ProxyURL is the proxy used when Egress is "proxy" (or when "auto" finds
	// no direct group and the host sets no global proxy).
	ProxyURL string `yaml:"proxy-url"`
	// UsageURL overrides the usage endpoint derived from each base URL. Useful
	// when the upstream moves the endpoint.
	UsageURL string `yaml:"usage-url"`
	// Session, when set, is sent as the x-opencode-session header on usage
	// probes. The endpoint does not require it today, but the gateway's other
	// endpoints do.
	Session string `yaml:"session"`
	// TimeoutSeconds bounds one /usage probe. Defaults to 20.
	TimeoutSeconds int `yaml:"timeout-seconds"`
	// CacheSeconds is how long a quota snapshot is reused before the page hits
	// the upstream again. Defaults to 10; "刷新" on the page bypasses it.
	CacheSeconds int `yaml:"cache-seconds"`
}

func defaultConfig() Config {
	return Config{
		Hosts:          []string{"opencode.ai"},
		Egress:         egressAuto,
		TimeoutSeconds: defaultTimeoutSeconds,
		CacheSeconds:   defaultCacheSeconds,
	}
}

// parseConfig decodes the plugin configuration block. Unknown fields (such as
// the host-injected enabled/priority flags) are ignored.
func parseConfig(raw []byte) (Config, error) {
	cfg := defaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return cfg, fmt.Errorf("解析插件配置失败: %w", errUnmarshal)
	}
	cfg.normalize()
	return cfg, nil
}

// normalize fills defaults, trims whitespace and rejects unknown egress modes
// by falling back to auto instead of failing the whole page.
func (c *Config) normalize() {
	c.ConfigPath = strings.TrimSpace(c.ConfigPath)
	c.ProxyURL = normalizeProxy(c.ProxyURL)
	c.UsageURL = strings.TrimSpace(c.UsageURL)
	c.Session = strings.TrimSpace(c.Session)

	hosts := make([]string, 0, len(c.Hosts))
	seen := make(map[string]struct{}, len(c.Hosts))
	for _, host := range c.Hosts {
		host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))
		if host == "" {
			continue
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		hosts = []string{"opencode.ai"}
	}
	c.Hosts = hosts

	switch strings.ToLower(strings.TrimSpace(c.Egress)) {
	case egressDirect:
		c.Egress = egressDirect
	case egressProxy:
		c.Egress = egressProxy
	default:
		c.Egress = egressAuto
	}

	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = defaultTimeoutSeconds
	}
	if c.TimeoutSeconds > maxTimeoutSeconds {
		c.TimeoutSeconds = maxTimeoutSeconds
	}
	if c.CacheSeconds < 0 {
		c.CacheSeconds = 0
	}
	if c.CacheSeconds > maxCacheSeconds {
		c.CacheSeconds = maxCacheSeconds
	}
}

// Timeout is the per-probe HTTP timeout.
func (c Config) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Cache is how long a snapshot is reused.
func (c Config) Cache() time.Duration {
	return time.Duration(c.CacheSeconds) * time.Second
}

// normalizeProxy maps the host's "direct"/"none" spelling to "no proxy".
func normalizeProxy(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "direct", "none", "off", "no":
		return ""
	}
	return trimmed
}

// HostAllowed reports whether a base URL belongs to one of the configured hosts.
func (c Config) HostAllowed(baseURL string) bool {
	host := hostOf(baseURL)
	if host == "" {
		return false
	}
	for _, allowed := range c.Hosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}
