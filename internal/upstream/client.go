// Package upstream performs the actual OpenCode Go HTTP calls. It owns the
// per-model proxy decision, credential rotation, stream and non-stream
// requests, and the mapping of upstream failures onto plugin errors.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/abi"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/config"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

// maxErrorBody caps how much upstream error text is surfaced downstream.
const maxErrorBody = 2048

// endpointUnavailableRetryDelay spaces the single retry for a 503
// "Endpoint is unavailable" response far enough apart that a new upstream
// connection can land on a healthy gateway node.
const endpointUnavailableRetryDelay = 300 * time.Millisecond

// admissionRetryMaxDelay bounds the single wait before retrying a transient
// admission refusal. The upstream hint is a handful of seconds in practice;
// the cap keeps a stale or hostile hint from parking a request for minutes.
const admissionRetryMaxDelay = 30 * time.Second

// admissionRefusalHint matches the retry hint OpenCode Go appends to an
// admission refusal: "[ttft_admission_refused] the cell is at capacity; retry
// shortly (after 16 s)".
var admissionRefusalHint = regexp.MustCompile(`after\s+([0-9]{1,4})\s*s`)

// admissionRefusalRetryDelay reports how long to wait before retrying a 429
// that is an admission refusal rather than a verdict on the credential.
//
// The refusal means the gateway cell that would run this request is full, not
// that the key ran out of quota, and the upstream says when to come back.
// CLIProxyAPI cannot make that distinction: it cools the credential on every
// 429, so two refusals across a two-key pool black out the model with "all
// credentials are cooling down". Absorbing the refusal here keeps a capacity
// blip from turning into a credential cooldown, which is strictly worse for
// every following request on the same key.
func admissionRefusalRetryDelay(body []byte) (time.Duration, bool) {
	if !bytes.Contains(body, []byte("ttft_admission_refused")) {
		return 0, false
	}
	delay := endpointUnavailableRetryDelay
	if match := admissionRefusalHint.FindSubmatch(body); match != nil {
		if seconds, errAtoi := strconv.Atoi(string(match[1])); errAtoi == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		}
	}
	if delay > admissionRetryMaxDelay {
		delay = admissionRetryMaxDelay
	}
	return delay, true
}

// anthropicVersion is required by the Anthropic Messages endpoint.
const anthropicVersion = "2023-06-01"

// Call is one upstream invocation.
type Call struct {
	Role roles.Role
	// Model is the id OpenCode Go expects.
	Model string
	// RequestedModel is the id the client asked for. When it differs from Model
	// the body's model field is rewritten, which is how aliases are served.
	RequestedModel string
	Stream         bool
	Body           []byte
	Session        string
	// UserAgent is the inbound client's User-Agent, forwarded verbatim so the
	// upstream sees the real client instead of a synthesised plugin identity.
	// The plugin owns exactly one request header of its own: x-opencode-session.
	UserAgent string
	// Credential is the selected upstream credential chosen by CPA's AuthManager.
	Credential config.Key
}

// Result is a completed non-streaming call.
type Result struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	KeyName    string
	Repairs    []string
}

// Stream is an in-flight streaming call. The caller must close Body.
type Stream struct {
	StatusCode int
	Headers    http.Header
	Body       io.ReadCloser
	KeyName    string
	Repairs    []string
}

const (
	repairSessionTTL   = 2 * time.Hour
	repairSessionLimit = 4096
)

// Client is the shared upstream client.
type Client struct {
	settings atomic.Pointer[config.Settings]

	mu      sync.Mutex
	clients map[string]*http.Client

	modelsCache modelsCache

	repairMu       sync.Mutex
	repairSessions map[string]time.Time
}

// New builds a client for the supplied settings.
func New(settings config.Settings) *Client {
	client := &Client{
		clients:        map[string]*http.Client{},
		repairSessions: map[string]time.Time{},
	}
	client.Update(settings)
	return client
}

// Update applies new settings.
func (c *Client) Update(settings config.Settings) {
	copied := settings
	c.settings.Store(&copied)
	c.mu.Lock()
	c.clients = map[string]*http.Client{}
	c.mu.Unlock()
}

// Settings returns the active settings.
func (c *Client) Settings() config.Settings {
	if current := c.settings.Load(); current != nil {
		return *current
	}
	return config.Default()
}

func (c *Client) reasoningRepairWanted(session string) bool {
	trimmed := strings.TrimSpace(session)
	if trimmed == "" {
		return false
	}
	now := time.Now()
	c.repairMu.Lock()
	defer c.repairMu.Unlock()
	if c.repairSessions == nil {
		return false
	}
	expiry, exists := c.repairSessions[trimmed]
	if !exists {
		return false
	}
	if now.After(expiry) {
		delete(c.repairSessions, trimmed)
		return false
	}
	c.repairSessions[trimmed] = now.Add(repairSessionTTL)
	return true
}

func (c *Client) markReasoningRepair(session string) {
	trimmed := strings.TrimSpace(session)
	if trimmed == "" {
		return
	}
	now := time.Now()
	c.repairMu.Lock()
	defer c.repairMu.Unlock()
	if c.repairSessions == nil {
		c.repairSessions = map[string]time.Time{}
	}
	if len(c.repairSessions) >= repairSessionLimit {
		for s, exp := range c.repairSessions {
			if now.After(exp) {
				delete(c.repairSessions, s)
			}
		}
		if len(c.repairSessions) >= repairSessionLimit {
			for s := range c.repairSessions {
				delete(c.repairSessions, s)
				break
			}
		}
	}
	c.repairSessions[trimmed] = now.Add(repairSessionTTL)
}

func (c *Client) resolveCredential(call Call, settings config.Settings) (config.Key, error) {
	if strings.TrimSpace(call.Credential.Key) != "" {
		return call.Credential, nil
	}
	enabled := settings.EnabledKeys()
	if len(enabled) == 0 {
		return config.Key{}, abi.StatusErrorf(http.StatusUnauthorized, "no_api_key",
			"OpenCode Go 没有可用的 API Key，请在插件页面的 Key 池中添加")
	}
	if len(enabled) == 1 {
		// A single-key deployment works without a host auth record. This is not
		// key selection: there is only one possible key.
		return enabled[0], nil
	}
	// Multiple keys are selected by CPA's AuthManager through the projected auth
	// files. Picking the first configured key here was the v1.10-v2.0.2
	// failover bug, so refuse instead of silently routing to a single account.
	return config.Key{}, abi.StatusErrorf(http.StatusServiceUnavailable, "auth_not_selected",
		"CPA 没有为本请求选择认证文件；多 Key 配置必须由 CPA 的 AuthManager 调度，"+
			"请确认认证文件已投影且宿主路由/重试配置允许切换 Key")
}

func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// Execute performs a non-streaming upstream call with the selected credential.
func (c *Client) Execute(ctx context.Context, call Call) (Result, error) {
	settings := c.Settings()
	credential, errCred := c.resolveCredential(call, settings)
	if errCred != nil {
		return Result{}, errCred
	}

	response, repairs, errDo := c.do(ctx, call, credential, settings)
	if errDo != nil {
		return Result{Repairs: repairs}, errDo
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		body, errRead := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if errRead != nil {
			return Result{Repairs: repairs}, abi.StatusErrorf(http.StatusBadGateway, "upstream_read_failed",
				"读取 OpenCode Go 响应失败: %v", errRead)
		}
		return Result{
			StatusCode: response.StatusCode,
			Headers:    response.Header,
			Body:       body,
			KeyName:    credential.Name,
			Repairs:    repairs,
		}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	_ = response.Body.Close()
	return Result{Headers: response.Header, Repairs: repairs}, upstreamError(response.StatusCode, body, credential.Name)
}

// Stream performs a streaming upstream call with the selected credential.
func (c *Client) Stream(ctx context.Context, call Call) (*Stream, error) {
	settings := c.Settings()
	call.Stream = true
	credential, errCred := c.resolveCredential(call, settings)
	if errCred != nil {
		return nil, errCred
	}

	response, repairs, errDo := c.do(ctx, call, credential, settings)
	if errDo != nil {
		return nil, errDo
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return &Stream{
			StatusCode: response.StatusCode,
			Headers:    response.Header,
			Body:       response.Body,
			KeyName:    credential.Name,
			Repairs:    repairs,
		}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	_ = response.Body.Close()
	return nil, upstreamError(response.StatusCode, body, credential.Name)
}

func (c *Client) do(ctx context.Context, call Call, credential config.Key, settings config.Settings) (*http.Response, []string, error) {
	endpoint := settings.EndpointURL(call.Role, call.Model, call.Stream)
	client, errClient := c.httpClient(settings, call)
	if errClient != nil {
		return nil, nil, errClient
	}

	devMode := settings.DeveloperRoleMode()
	replayMode := settings.ReasoningReplayMode()
	stickyReasoning := c.reasoningRepairWanted(call.Session)

	body := call.Body
	var appliedRepairs []string

	if call.Role.StripsStreamField() {
		if devMode == config.DeveloperRoleRewrite {
			body = normalizeDeveloperRoles(call.Role, body, true)
		}
		if next, changed := normalizeTopP(call.Role, body); changed {
			body = next
			appliedRepairs = append(appliedRepairs, "top-p-range")
		}
		if replayMode == config.ReasoningReplayAlways || replayMode == config.ReasoningReplayAuto || (replayMode == config.ReasoningReplayRetryOnly && stickyReasoning) {
			mode := replayMode
			if mode == config.ReasoningReplayRetryOnly {
				mode = config.ReasoningReplayAuto
			}
			next := ensureReasoningContent(call.Role, body, mode)
			if !bytes.Equal(next, body) {
				body = next
				if replayMode == config.ReasoningReplayRetryOnly {
					appliedRepairs = append(appliedRepairs, "reasoning-replay")
				}
			}
		}
		body = rewriteModelField(body, call.RequestedModel, call.Model)
		body = forceStreamFlag(body, call.Stream)
	}

	transientRetried := false
	unavailableRetried := false
	admissionRetried := false
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// A retry must not reuse the connection that just failed: the
			// gateway balances per connection, and a poisoned node kept
			// rejecting a valid json_schema body on the same socket while the
			// next connection succeeded.
			client.CloseIdleConnections()
		}
		request, errRequest := newUpstreamRequest(ctx, endpoint, body, call, credential, settings)
		if errRequest != nil {
			return nil, appliedRepairs, errRequest
		}
		response, errDo := client.Do(request)
		if errDo != nil {
			return nil, appliedRepairs, abi.StatusErrorf(http.StatusBadGateway, "upstream_unreachable",
				"连接 OpenCode Go 失败: %v", errDo)
		}
		// OpenCode Go answers a transient gateway/node problem with 503
		// server_error "Endpoint is unavailable." CLIProxyAPI cools the whole
		// credential on every transient 5xx, so trying the same key once more
		// on a fresh connection is cheaper than a model-wide blackout. This is
		// safe for streaming too: a 503 response never carried any content.
		if response.StatusCode == http.StatusServiceUnavailable && !unavailableRetried && attempt < 2 {
			peeked, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
			_ = response.Body.Close()
			if requestScopedUpstreamRefusal(peeked) {
				unavailableRetried = true
				if errWait := waitRetry(ctx, endpointUnavailableRetryDelay); errWait != nil {
					return nil, appliedRepairs, abi.StatusErrorf(http.StatusRequestTimeout, "request_cancelled",
						"等待 OpenCode Go 重试时请求已结束: %v", errWait)
				}
				continue
			}
			response.Body = io.NopCloser(bytes.NewReader(peeked))
		}
		// A 429 is not always a credential verdict. OpenCode Go answers a full
		// gateway cell with "[ttft_admission_refused] the cell is at capacity"
		// plus a retry hint, and it occasionally hides the same gateway hiccup
		// behind an opaque 429 whose body is just {"model":"..."}. Neither is a
		// quota failure, but CLIProxyAPI cools the key on every 429; retrying
		// the same key once here keeps a capacity blip from escalating into a
		// cooldown that outlives the blip. Quota and auth 429s are untouched:
		// they must reach the host so it can cool the key and switch.
		if response.StatusCode == http.StatusTooManyRequests && !admissionRetried && attempt < 2 {
			peeked, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
			_ = response.Body.Close()
			delay, admission := admissionRefusalRetryDelay(peeked)
			if !admission && isTransientRejection(peeked) {
				delay, admission = endpointUnavailableRetryDelay, true
			}
			if admission {
				admissionRetried = true
				if errWait := waitRetry(ctx, delay); errWait != nil {
					return nil, appliedRepairs, abi.StatusErrorf(http.StatusRequestTimeout, "request_cancelled",
						"等待 OpenCode Go 重试时请求已结束: %v", errWait)
				}
				continue
			}
			response.Body = io.NopCloser(bytes.NewReader(peeked))
		}
		if !isRepairableRejection(response.StatusCode) || attempt == 2 || !call.Role.StripsStreamField() {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if containsString(appliedRepairs, "reasoning-replay") {
					c.markReasoningRepair(call.Session)
				}
			}
			return response, appliedRepairs, nil
		}

		peeked, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		_ = response.Body.Close()

		repaired := false
		// 1. Check developer role rejection. Besides the explicit wordings the
		// gateway can hide the same rejection behind an opaque 422 (for example
		// DeepSeek-V4-Pro answers "Endpoint is unavailable" for a developer
		// message). In that case the request body is the only reliable signal.
		developerRejected := isDeveloperRoleRejection(peeked)
		if !developerRejected && response.StatusCode == http.StatusUnprocessableEntity {
			developerRejected = bytes.Contains(body, []byte(`"developer"`))
		}
		if devMode == config.DeveloperRoleRetryOnly && !containsString(appliedRepairs, "developer-role") && developerRejected {
			next := normalizeDeveloperRoles(call.Role, body, true)
			if !bytes.Equal(next, body) {
				body = next
				appliedRepairs = append(appliedRepairs, "developer-role")
				repaired = true
			}
		}

		// 2. If not repaired by developer role, check reasoning replay 400 rejection
		if !repaired && replayMode != config.ReasoningReplayOff && !containsString(appliedRepairs, "reasoning-replay") && isReasoningReplayRejection(peeked) {
			next := ensureReasoningContent(call.Role, body, config.ReasoningReplayAlways)
			if !bytes.Equal(next, body) {
				body = next
			}
			appliedRepairs = append(appliedRepairs, "reasoning-replay")
			repaired = true
		}

		// 3. A bare or "feature unavailable" 400 is a gateway glitch; retry the
		// unchanged body once before surfacing it to the client.
		if !repaired && !transientRetried && isTransientRejection(peeked) {
			transientRetried = true
			repaired = true
		}

		if !repaired {
			// Not a recognized recoverable rejection, or repair already applied once.
			response.Body = io.NopCloser(bytes.NewReader(peeked))
			return response, appliedRepairs, nil
		}
	}

	return nil, appliedRepairs, errors.New("upstream retry ladder exhausted")
}

// isRepairableRejection reports whether an upstream rejection may be caused by
// a request body the plugin can repair (developer role, reasoning replay).
//
// The OpenCode Go gateway does not use one status code for body validation
// failures: the same "developer role is not supported" rejection arrives as
// 400 from some nodes and as 422 from others. 4xx codes outside these two are
// left untouched so authentication, quota and routing errors still surface.
func isRepairableRejection(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

// waitRetry sleeps for d unless the caller's context ends first.
func waitRetry(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// newUpstreamRequest builds one upstream HTTP request. It is called once per
// attempt so the retry in do() always starts from a fresh body reader.
func newUpstreamRequest(ctx context.Context, endpoint string, body []byte, call Call, credential config.Key, settings config.Settings) (*http.Request, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		return nil, abi.StatusErrorf(http.StatusInternalServerError, "bad_request",
			"构造 OpenCode Go 请求失败: %v", errRequest)
	}

	headers := request.Header
	headers.Set("Content-Type", "application/json")
	headers.Set("Authorization", "Bearer "+credential.Key)
	headers.Set("Accept", acceptHeader(call.Stream))
	if userAgent := strings.TrimSpace(call.UserAgent); userAgent != "" {
		headers.Set("User-Agent", userAgent)
	}
	if session := strings.TrimSpace(call.Session); session != "" {
		headers.Set("x-opencode-session", session)
	}
	if call.Role.UsesAPIKeyHeader() {
		headers.Set("x-goog-api-key", credential.Key)
	}
	if call.Role.UsesAnthropicVersion() {
		headers.Set("anthropic-version", anthropicVersion)
	}
	for name, value := range settings.ExtraHeaders {
		if strings.TrimSpace(name) == "" {
			continue
		}
		headers.Set(strings.TrimSpace(name), value)
	}
	return request, nil
}

func acceptHeader(stream bool) string {
	if stream {
		return "text/event-stream"
	}
	return "application/json"
}

// httpClient returns a client whose transport honours the per-model proxy mode.
// Clients are cached per proxy URL so connection pools are reused.
func (c *Client) httpClient(settings config.Settings, call Call) (*http.Client, error) {
	proxyURL, useProxy := settings.ResolvedProxyURL(call.Model)
	cacheKey := "direct"
	if useProxy {
		cacheKey = proxyURL
	}

	c.mu.Lock()
	cached, ok := c.clients[cacheKey]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}

	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	// Proxy nil means "never consult the environment"; setting Proxy here is the
	// only way to make an explicit per-model direct mode stick.
	if useProxy && proxyURL != "" {
		parsed, errParse := url.Parse(proxyURL)
		if errParse != nil {
			return nil, abi.StatusErrorf(http.StatusInternalServerError, "bad_proxy",
				"解析代理地址 %q 失败: %v", proxyURL, errParse)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	client := &http.Client{Transport: transport, Timeout: settings.Timeout()}
	c.mu.Lock()
	c.clients[cacheKey] = client
	c.mu.Unlock()
	return client, nil
}

func retryableStatus(status int) bool {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusTooManyRequests, status == http.StatusRequestTimeout,
		status == http.StatusBadGateway, status == http.StatusServiceUnavailable,
		status == http.StatusGatewayTimeout:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}

// upstreamError converts an upstream failure into a downstream error that keeps
// the upstream status and message.
func upstreamError(status int, body []byte, keyName string) error {
	message := summarizeUpstreamError(body)
	if message == "" {
		message = http.StatusText(status)
	}
	if status == http.StatusForbidden && requestScopedUpstreamRefusal(body) {
		// OpenCode Go refuses some requests with 403 server_error (for example
		// when its provider's safety policy rejects the conversation). The
		// refusal belongs to the request, not to the credential: retrying
		// another key cannot help, and CLIProxyAPI treats every 403 as a
		// credential fault and cools the key for 30 minutes. That turns one
		// refused prompt into a blackout for every other user of the model, so
		// report it as a non-retryable client error instead.
		return abi.StatusErrorf(http.StatusBadRequest, "upstream_refused",
			"OpenCode Go 上游拒绝了该请求（不是密钥或网络问题，通常是上游内容安全策略或端点策略）: %s", message)
	}
	if keyName != "" && retryableStatus(status) {
		message = fmt.Sprintf("[%s] %s", keyName, message)
	}
	return abi.StatusErrorf(status, "upstream_error", "%s", message)
}

// requestScopedUpstreamRefusal reports whether a 403 body is the gateway
// refusing this request rather than rejecting the credential. OpenCode Go
// answers provider-side refusals with
// {"error":{"type":"server_error","message":"Upstream request failed:
// Endpoint is unavailable."}}. Authentication and permission failures use a
// different error type, so they keep their 403 and their failover.
func requestScopedUpstreamRefusal(body []byte) bool {
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(body, &envelope); errUnmarshal != nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(envelope.Error.Type), "server_error") {
		return false
	}
	return strings.Contains(envelope.Error.Message, "Endpoint is unavailable")
}

// summarizeUpstreamError extracts the human-readable message from an upstream
// error body while keeping the raw payload when it is not JSON.
func summarizeUpstreamError(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var generic map[string]any
	if errUnmarshal := json.Unmarshal([]byte(trimmed), &generic); errUnmarshal == nil {
		if inner, ok := generic["error"].(map[string]any); ok {
			if message, ok := inner["message"].(string); ok && message != "" {
				if kind, ok := inner["type"].(string); ok && kind != "" {
					return kind + ": " + message
				}
				return message
			}
		}
		if message, ok := generic["message"].(string); ok && message != "" {
			return message
		}
	}
	if len(trimmed) > maxErrorBody {
		return trimmed[:maxErrorBody]
	}
	return trimmed
}

// rewriteModelField replaces the body's model id so an alias resolves to the
// real upstream model. It is a no-op when the ids already match.
func rewriteModelField(body []byte, requested, upstream string) []byte {
	requested = strings.TrimSpace(requested)
	upstream = strings.TrimSpace(upstream)
	if requested == "" || upstream == "" || requested == upstream {
		return body
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &object); errUnmarshal != nil {
		return body
	}
	current, ok := object["model"]
	if !ok {
		return body
	}
	var decoded string
	if errDecode := json.Unmarshal(current, &decoded); errDecode != nil || decoded != requested {
		return body
	}
	encoded, errMarshal := json.Marshal(upstream)
	if errMarshal != nil {
		return body
	}
	object["model"] = json.RawMessage(encoded)
	out, errMarshalAll := json.Marshal(object)
	if errMarshalAll != nil {
		return body
	}
	return out
}

// forceStreamFlag keeps the body's stream field in sync with the transport.
// The rewrite is skipped when it cannot change the outcome so that large
// payloads are not re-encoded on every request.
func forceStreamFlag(body []byte, stream bool) []byte {
	hasField := bytes.Contains(body, []byte(`"stream"`))
	if !stream && !hasField {
		return body
	}
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &object); errUnmarshal != nil {
		return body
	}
	want := []byte("true")
	if !stream {
		want = []byte("false")
	}
	if existing, ok := object["stream"]; ok && bytes.Equal(bytes.TrimSpace(existing), want) {
		return body
	}
	object["stream"] = json.RawMessage(want)
	encoded, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return body
	}
	return encoded
}
