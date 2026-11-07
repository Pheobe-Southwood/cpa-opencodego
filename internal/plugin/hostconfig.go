package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// HostGroup is one api-keys group found in the host configuration whose base
// URL points at an OpenCode Go host. The plugin never routes through it; the
// group is reported so the page can explain which models a key serves and which
// way that group's traffic leaves the machine.
type HostGroup struct {
	Key      string      `json:"key"`
	Family   string      `json:"family"`
	Name     string      `json:"name"`
	BaseURL  string      `json:"base_url"`
	Priority int         `json:"priority,omitempty"`
	Disabled bool        `json:"disabled,omitempty"`
	Egress   string      `json:"egress"`
	KeyCount int         `json:"key_count"`
	Models   []HostModel `json:"models,omitempty"`
}

// HostModel is one model entry of a group, as configured in the host.
type HostModel struct {
	Name        string `json:"name"`
	Alias       string `json:"alias,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// HostKey is one credential line of a group.
type HostKey struct {
	APIKey   string
	ProxyURL string
}

// Credential is one unique OpenCode Go key. A key can be listed by several
// groups (the native migration splits direct and proxy egress into separate
// groups while sharing the key pool), so the credential keeps the list of groups
// that reference it and the egress chosen for the usage probe.
type Credential struct {
	ID        string   `json:"id"`
	Key       string   `json:"-"`
	Masked    string   `json:"masked"`
	Egress    string   `json:"egress"`
	GroupKeys []string `json:"groups"`
}

// HostConfig is the read-only projection of the host configuration the plugin
// needs: the OpenCode Go groups, the unique keys behind them, and the global
// proxy that an inheriting group uses.
type HostConfig struct {
	Path        string
	GlobalProxy string
	Groups      []HostGroup
	Credentials []Credential
	UsageURL    string
	Warnings    []string
	LoadedAt    time.Time

	keys map[string][]HostKey
}

// hostSource loads and caches the host configuration. It re-reads the file when
// its size or modification time changes, so editing config.yaml is enough to
// refresh the page; nothing is ever written back.
type hostSource struct {
	mu      sync.Mutex
	config  Config
	path    string
	stamp   fileStamp
	parsed  *HostConfig
	lastErr error
}

type fileStamp struct {
	path    string
	modTime time.Time
	size    int64
}

func newHostSource(config Config) *hostSource {
	return &hostSource{config: config}
}

// Load returns the parsed host configuration, reusing the previous parse while
// the file is unchanged. force skips the file-stamp check.
func (s *hostSource) Load(force bool) (*HostConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, errPath := discoverConfigPath(s.config.ConfigPath)
	if errPath != nil {
		s.lastErr = errPath
		return nil, errPath
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		s.lastErr = fmt.Errorf("读取宿主配置失败: %w", errStat)
		return nil, s.lastErr
	}
	stamp := fileStamp{path: path, modTime: info.ModTime(), size: info.Size()}
	if !force && s.parsed != nil && s.stamp == stamp && s.path == path {
		return s.parsed, nil
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		s.lastErr = fmt.Errorf("读取宿主配置失败: %w", errRead)
		return nil, s.lastErr
	}
	parsed, errParse := parseHostConfig(raw, path, s.config)
	if errParse != nil {
		s.lastErr = errParse
		return nil, errParse
	}
	s.path = path
	s.stamp = stamp
	s.parsed = parsed
	s.lastErr = nil
	return parsed, nil
}

// Path reports the file the source last resolved, even when the parse failed.
func (s *hostSource) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// discoverConfigPath resolves the host configuration file: the explicit plugin
// setting first, then the host process argv, then environment overrides, then
// the well-known container locations.
func discoverConfigPath(override string) (string, error) {
	override = strings.TrimSpace(override)
	if override != "" {
		if info, errStat := os.Stat(override); errStat == nil && !info.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("config-path %q 不存在或不是文件", override)
	}

	candidates := make([]string, 0, 8)
	if argv := configPathFromArgs(os.Args); argv != "" {
		candidates = append(candidates, argv)
	}
	for _, name := range []string{"CPA_CONFIG_PATH", "CLIPROXY_CONFIG_PATH", "CLIPROXYAPI_CONFIG"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			candidates = append(candidates, value)
		}
	}
	candidates = append(candidates,
		"/CLIProxyAPI/conf/config.yaml",
		"/opt/cliproxyapi/conf/config.yaml",
		"/root/cliproxyapi/conf/config.yaml",
		"config.yaml",
	)
	for _, candidate := range candidates {
		if info, errStat := os.Stat(candidate); errStat == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到宿主 config.yaml（已尝试: %s）；请在插件配置里设置 config-path", strings.Join(candidates, ", "))
}

// configPathFromArgs finds the -config / --config argument of the host process.
// The plugin is loaded into the host process, so os.Args are the host's argv.
func configPathFromArgs(args []string) string {
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "-config" || arg == "--config":
			if index+1 < len(args) {
				return strings.TrimSpace(args[index+1])
			}
		case strings.HasPrefix(arg, "-config="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "-config="))
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
		}
	}
	return ""
}

// yamlFile mirrors only the host configuration fields the plugin reads. The
// v8 api-keys block is authoritative; the legacy top-level families are parsed
// as a fallback so a pre-v8 configuration still reports its keys.
type yamlFile struct {
	APIKeys *struct {
		Gemini       []yamlGroup `yaml:"gemini"`
		Claude       []yamlGroup `yaml:"claude"`
		Codex        []yamlGroup `yaml:"codex"`
		OpenAICompat []yamlGroup `yaml:"openai-compatibility"`
	} `yaml:"api-keys"`
	GeminiKey    []yamlGroup `yaml:"gemini-api-key"`
	ClaudeKey    []yamlGroup `yaml:"claude-api-key"`
	CodexKey     []yamlGroup `yaml:"codex-api-key"`
	OpenAICompat []yamlGroup `yaml:"openai-compatibility"`
	Requests     *struct {
		ProxyURL string `yaml:"proxy-url"`
	} `yaml:"requests"`
}

type yamlGroup struct {
	Name          string            `yaml:"name"`
	BaseURL       string            `yaml:"base-url"`
	Disabled      bool              `yaml:"disabled"`
	Priority      int               `yaml:"priority"`
	ProxyURL      string            `yaml:"proxy-url"`
	Headers       map[string]string `yaml:"headers"`
	Keys          []yamlKey         `yaml:"keys"`
	APIKey        string            `yaml:"api-key"`
	APIKeyEntries []yamlKey         `yaml:"api-key-entries"`
	Models        []yamlModel       `yaml:"models"`
}

type yamlKey struct {
	APIKey   string `yaml:"api-key"`
	ProxyURL string `yaml:"proxy-url"`
	Weight   *int   `yaml:"weight"`
}

type yamlModel struct {
	Name        string `yaml:"name"`
	Alias       string `yaml:"alias"`
	DisplayName string `yaml:"display-name"`
}

// parseHostConfig projects the raw YAML onto the read-only model.
func parseHostConfig(raw []byte, path string, config Config) (*HostConfig, error) {
	var file yamlFile
	if errUnmarshal := yaml.Unmarshal(raw, &file); errUnmarshal != nil {
		return nil, fmt.Errorf("解析宿主配置失败: %w", errUnmarshal)
	}

	out := &HostConfig{Path: path, LoadedAt: time.Now(), keys: make(map[string][]HostKey)}
	if file.Requests != nil {
		out.GlobalProxy = normalizeProxy(file.Requests.ProxyURL)
	}

	type family struct {
		name   string
		groups []yamlGroup
	}
	families := make([]family, 0, 4)
	if file.APIKeys != nil {
		families = append(families,
			family{"gemini", file.APIKeys.Gemini},
			family{"claude", file.APIKeys.Claude},
			family{"codex", file.APIKeys.Codex},
			family{"openai-compatibility", file.APIKeys.OpenAICompat},
		)
	} else {
		families = append(families,
			family{"gemini", file.GeminiKey},
			family{"claude", file.ClaudeKey},
			family{"codex", file.CodexKey},
			family{"openai-compatibility", file.OpenAICompat},
		)
	}

	seenKeys := make(map[string]struct{})
	usedGroupKeys := make(map[string]struct{})
	for _, item := range families {
		for _, group := range item.groups {
			projected, keys, okGroup := projectGroup(item.name, group, config)
			if !okGroup {
				continue
			}
			if _, duplicate := usedGroupKeys[projected.Key]; duplicate {
				projected.Key = fmt.Sprintf("%s#%d", projected.Key, len(out.Groups))
			}
			usedGroupKeys[projected.Key] = struct{}{}
			out.Groups = append(out.Groups, projected)
			out.keys[projected.Key] = keys
			if projected.Disabled {
				continue
			}
			for _, key := range keys {
				_, exists := seenKeys[key.APIKey]
				if exists {
					continue
				}
				seenKeys[key.APIKey] = struct{}{}
				out.Credentials = append(out.Credentials, Credential{
					ID:     credentialID(key.APIKey),
					Key:    key.APIKey,
					Masked: maskKey(key.APIKey),
				})
			}
		}
	}

	// Resolve each credential's egress and the groups it serves. A key is
	// reported once: its quota is a property of the key, not of the group that
	// happened to list it first.
	for index := range out.Credentials {
		credential := &out.Credentials[index]
		var refs []string
		for _, group := range out.Groups {
			if group.Disabled {
				continue
			}
			for _, key := range out.keys[group.Key] {
				if key.APIKey != credential.Key {
					continue
				}
				credential.GroupKeys = append(credential.GroupKeys, group.Key)
				refs = append(refs, keyEgress(key, group))
				break
			}
		}
		credential.Egress = resolveEgress(refs, config, out.GlobalProxy)
	}

	out.UsageURL = usageURLFor(out, config)
	if len(out.Groups) == 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%s 中没有 base-url 指向 %s 的 api-keys 分组", path, strings.Join(config.Hosts, ", ")))
	}
	return out, nil
}

// projectGroup projects one configured group. It returns ok=false for a group
// that does not belong to the OpenCode Go hosts.
func projectGroup(family string, group yamlGroup, config Config) (HostGroup, []HostKey, bool) {
	baseURL := strings.TrimSpace(group.BaseURL)
	if baseURL == "" || !config.HostAllowed(baseURL) {
		return HostGroup{}, nil, false
	}
	name := strings.TrimSpace(group.Name)
	if name == "" {
		name = family
	}
	key := family + ":" + name

	keys := make([]HostKey, 0, len(group.Keys)+1)
	appendKey := func(entry yamlKey) {
		apiKey := strings.TrimSpace(entry.APIKey)
		if apiKey == "" {
			return
		}
		keys = append(keys, HostKey{APIKey: apiKey, ProxyURL: strings.TrimSpace(entry.ProxyURL)})
	}
	for _, entry := range group.Keys {
		appendKey(entry)
	}
	for _, entry := range group.APIKeyEntries {
		appendKey(entry)
	}
	if len(keys) == 0 && strings.TrimSpace(group.APIKey) != "" {
		appendKey(yamlKey{APIKey: group.APIKey})
	}
	if len(keys) == 0 {
		return HostGroup{}, nil, false
	}

	models := make([]HostModel, 0, len(group.Models))
	for _, model := range group.Models {
		models = append(models, HostModel{
			Name:        strings.TrimSpace(model.Name),
			Alias:       strings.TrimSpace(model.Alias),
			DisplayName: strings.TrimSpace(model.DisplayName),
		})
	}

	// The group-level proxy-url is only one way to decide egress: the native
	// openai-compatibility migration puts proxy-url on every key instead, so the
	// group row reports the decision the keys actually make.
	egress := normalizeProxyEgress(group.ProxyURL)
	if egress == "" {
		direct := 0
		for _, key := range keys {
			if normalizeProxyEgress(key.ProxyURL) == "direct" {
				direct++
			}
		}
		switch {
		case direct == len(keys):
			egress = "direct"
		case direct > 0:
			egress = "mixed"
		}
	}

	return HostGroup{
		Key:      key,
		Family:   family,
		Name:     name,
		BaseURL:  baseURL,
		Priority: group.Priority,
		Disabled: group.Disabled,
		Egress:   egress,
		KeyCount: len(keys),
		Models:   models,
	}, keys, true
}

// normalizeProxyEgress keeps "direct" as a decision and folds "none" into it;
// an empty string means "inherit the global proxy".
func normalizeProxyEgress(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "":
		return ""
	case "direct", "none", "off", "no":
		return "direct"
	}
	return trimmed
}

// keyEgress is the per-key egress decision: a key-level proxy-url wins over the
// group-level one.
func keyEgress(key HostKey, group HostGroup) string {
	if value := normalizeProxyEgress(key.ProxyURL); value != "" {
		return value
	}
	return group.Egress
}

// resolveEgress turns the per-group decisions into the egress of one usage
// probe: "direct" (empty string) or a proxy URL.
func resolveEgress(refs []string, config Config, globalProxy string) string {
	switch config.Egress {
	case egressDirect:
		return ""
	case egressProxy:
		if config.ProxyURL != "" {
			return config.ProxyURL
		}
		return normalizeProxy(globalProxy)
	}
	// auto: follow the host's own split. Any direct group means direct, because
	// /usage answers a plain HTTP request and direct is the cheapest path; when
	// every group that lists the key inherits, use the host's global proxy.
	for _, ref := range refs {
		if ref == "direct" {
			return ""
		}
	}
	for _, ref := range refs {
		if ref != "" {
			return ref
		}
	}
	return normalizeProxy(globalProxy)
}

// usageURLFor derives the canonical usage endpoint. Every OpenCode Go family
// base URL (…/inference/go/v1, …/inference/go/google, …/inference/go/openai/v1,
// …/inference/go/anthropic) resolves to the same …/inference/go/v1/usage.
func usageURLFor(hostConfig *HostConfig, config Config) string {
	if config.UsageURL != "" {
		return config.UsageURL
	}
	if hostConfig == nil {
		return ""
	}
	for _, group := range hostConfig.Groups {
		if group.Disabled {
			continue
		}
		if endpoint := usageURLForBase(group.BaseURL); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

// usageURLForBase maps one OpenCode Go base URL onto the usage endpoint.
func usageURLForBase(baseURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil || parsed.Host == "" {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	for _, marker := range []string{"/inference/go", "/zen/go"} {
		if index := strings.Index(path, marker); index >= 0 {
			parsed.Path = path[:index+len(marker)] + "/v1/usage"
			return parsed.String()
		}
	}
	if path == "" {
		parsed.Path = "/usage"
		return parsed.String()
	}
	parsed.Path = path + "/usage"
	return parsed.String()
}

// hostOf returns the lower-case hostname of a URL, or "" when it is not one.
func hostOf(rawURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(rawURL))
	if errParse != nil || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// credentialID is the stable, non-secret id the page and the JSON payload use.
func credentialID(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:6])
}

// maskKey keeps enough of a key to identify it in the pool while never
// rendering a usable secret: oc_sk_cd1a…rq2N4.
func maskKey(apiKey string) string {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 16 {
		return trimmed[:min(4, len(trimmed))] + "…"
	}
	return trimmed[:10] + "…" + trimmed[len(trimmed)-4:]
}
