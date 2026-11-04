// Package dashboard tests assert the properties the operator cares about, so a
// later edit cannot silently bring back a read-only page, a bottom-anchored bar,
// or a save that writes values the page never read.
package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
)

func page(t *testing.T) string {
	t.Helper()
	return string(Page("cpa-opencodego", "chat"))
}

func TestPageSubstitutesPlaceholders(t *testing.T) {
	document := page(t)
	for _, placeholder := range []string{"__PLUGIN_ID__", "__ROLE__", "__BRAND_NAME__", "__BRAND_FAVICON__", "__BRAND_MARK__", "__REPO_URL__"} {
		if strings.Contains(document, placeholder) {
			t.Errorf("placeholder %s was not substituted", placeholder)
		}
	}
	if !strings.Contains(document, `var PLUGIN_ID = "cpa-opencodego"`) {
		t.Error("plugin id missing from the document")
	}
	if !strings.Contains(document, `var ROLE = "chat"`) {
		t.Error("role missing from the document")
	}
}

// One product, four protocol faces: the four rows the host's plugin list shows
// for this provider have to look like one thing, so the page and the rows share
// the same mark, the same product name and the same repository link.
func TestPageCarriesTheSharedBrand(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`<title>` + brand.Name + `</title>`,
		`<link rel="icon" type="image/svg+xml" href="` + brand.Logo + `">`,
		`<span class="brand-mark" aria-hidden="true">` + brand.Markup + `</span>`,
		`<h1>` + brand.Name + `</h1>`,
		`var REPO_URL = '` + brand.Repository + `';`,
		".brand-mark{",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The mark is a real square icon, not a placeholder glyph.
	if !strings.Contains(document, `viewBox="0 0 512 512"`) {
		t.Error("the inlined brand mark lost its viewBox")
	}
}

// The plugin list in 插件管理 shows one row per shared library. The status tab
// is where the operator manages them as one product: every face names its
// protocol, its plugin id, the models that depend on it and whether it owns the
// configuration.
func TestPageManagesFourProtocolFaces(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`<h2>协议面</h2>`,
		`id="siblingGrid"`,
		`id="faceHint"`,
		"四个 .so 必须同版本安装",
		"function renderFace(face){",
		"face.main ? '配置入口' : '零配置'",
		"'无需配置：由 <code>cpa-opencodego</code> 统一维护，插件管理里只控制启用。'",
		"'本面承载侧边栏页面与全部配置：Key、模型、设置都在这里改。'",
		"face.protocol",
		"face.endpoint",
		"face.model_count",
		"face.plugin_id",
		".face-main{",
		".face-tag{",
		"['协议面', (state && state.siblings ? state.siblings.length : 0) + ' 个'",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The old card listed bare instances; the faces replaced it.
	if strings.Contains(document, `<h2>插件实例</h2>`) {
		t.Error("the instances card must be replaced by the protocol-face card")
	}
}

// The page is split into tabs, and the tab the operator opens it for — the key
// quota of every account — is the one shown first.
func TestPageTabsStartOnAccounts(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`data-tab="accounts"`, `data-tab="stats"`, `data-tab="requests"`, `data-tab="models"`, `data-tab="status"`, `data-tab="settings"`,
		`id="pane-accounts"`, `id="pane-stats"`, `id="pane-requests"`, `id="pane-models"`, `id="pane-status"`, `id="pane-settings"`,
		`role="tablist"`, `role="tabpanel"`,
		// Accounts is the only pane that starts visible.
		`class="pane active" id="pane-accounts"`,
		`id="pane-models" role="tabpanel" aria-labelledby="tab-models" hidden`,
		"var TABS = ['accounts','stats','requests','models','status','settings'];",
		"var tab = 'accounts';",
		"return TABS.indexOf(name) >= 0 ? name : 'accounts';",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Each tab carries a dot that the chrome renderer lights up per pane.
	for _, want := range []string{`id="tab-accounts"`, `class="dot"`, "function showTab(name, keepScroll)"} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// The 请求日志 tab shows one row per upstream attempt, with the TPS/TTFT
// numbers the plugin measures itself, and states what it does not store.
func TestPageHasRequestLogTab(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`<h2>逐条请求</h2>`,
		`id="requestTable"`,
		`id="requestSummary"`,
		`id="requestsFamily"`,
		`id="requestsKey"`,
		`id="requestsModel"`,
		`id="requestsFailed"`,
		`id="requestsAuto"`,
		`id="refreshRequests"`,
		`id="requestsFootnote"`,
		`data-label="生成 TPS"`,
		`data-label="端到端 TPS"`,
		`data-label="TTFT"`,
		"/api/requests?",
		"生成 TPS</strong>",
		"端到端 TPS</strong>",
		"不记录提示词、响应正文、Key 或原始会话 ID",
		"x-opencode-log-id",
		"function renderRequests(",
		"function loadRequests(",
		"function requestRow(",
		"scheduleRequestsTimer",
		"entry.incomplete",
		"entry.repairs",
		"entry.canceled",
		"取消",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// The protocol settings are editable from the page so a v1 deployment can be
// moved to the per-family endpoints without touching config.yaml by hand.
func TestPageExposesProtocolSettings(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`<h2>协议与兼容</h2>`,
		`id="cfgEndpointLayout"`,
		`id="cfgDeveloperRole"`,
		`id="cfgReasoningReplay"`,
		`value="retry-only"`,
		`cfgReasoningReplay:'reasoning-replay'`,
		`id="cfgRequestLog"`,
		`id="endpointHint"`,
		`'endpoint-layout':'端点布局'`,
		`cfgEndpointLayout:'endpoint-layout'`,
		`cfgDeveloperRole:'developer-role'`,
		`state.endpoint_layout`,
		`state.reasoning_replay`,
		`state.models_url`,
		`state.usage_url`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// Key quota is the first thing on the accounts tab: remaining percent per
// window, per key, straight from the plugin's quota resource.
func TestPageAccountsShowRemainingQuota(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`id="accountStats"`, `id="keyList"`, `id="refreshQuota"`, `/api/quota`,
		"function keyViews()", "function quotaFor(name)", "function quotaRow(label, percent, resetsAt)",
		`">剩余 ' + left.toFixed(0) + '%`, "最低剩余",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// There is no read-only mode: without a stored credential the operator can still
// save, and the page asks for the WebUI password in a dialog at that moment.
func TestPageAsksForThePasswordInsteadOfGoingReadOnly(t *testing.T) {
	document := page(t)
	// The old page had a read-only state driven by the credential store; the
	// word and the state are both gone.
	for _, forbidden := range []string{"READONLY_NOTICE", "只读模式", "setReadOnly", "'只读'"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("the page must not contain %q", forbidden)
		}
	}
	for _, want := range []string{
		`id="credModal"`, `id="credInput"`, `id="credRemember"`, `id="credSubmit"`, `id="credCancel"`,
		"function askCredential(hint, errorMessage)", "function ensureCredential(hint)",
		"function submitCredential()", "function cancelCredential()",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The credential the centre stored is still reused when it exists; the typed
	// one lives in the browser tab session, never in the page or the plugin.
	if !strings.Contains(document, "cli-proxy-auth") {
		t.Error("the page must read the management centre credential store")
	}
	if !strings.Contains(document, "'managementKey'") {
		t.Error("the page must keep the legacy credential fallback")
	}
	if !strings.Contains(document, "sessionStorage.setItem(CRED_SESSION, value)") {
		t.Error("a remembered password must only live in the tab session")
	}
	if !strings.Contains(document, "连续输错 5 次会临时封禁当前 IP") {
		t.Error("the dialog must warn about the management key lockout")
	}
	// The write path is the host management API and it always carries a bearer.
	if !strings.Contains(document, "managementRequest(credential, CONFIG_PATH") {
		t.Error("the save must go through the management API with the credential")
	}
}

// The save button is the page's only call to action: it lights up as soon as the
// working copy differs from the stored configuration.
func TestSaveButtonLightsUpWhenDirty(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`id="save" class="save-btn"`,
		"save.disabled = saveBusy || count === 0;",
		"if(count > 0 && !saveBusy){ save.classList.add('is-dirty'); }",
		"function isDirty(){ return countEdits() > 0; }",
		"function countEdits(){",
		"function renderChrome(){",
		"is-dirty",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The lit state is a real style rule, and it stops animating when the
	// operator asked for reduced motion.
	for _, want := range []string{".save-btn.is-dirty{", "@keyframes cta-glow", "prefers-reduced-motion"} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing the style %q", want)
		}
	}
}

// A save merges the recorded edits into the configuration the host currently
// holds, so the page never has to have read — and never rewrites — the stored
// key secrets.
func TestPageMergesEditsInsteadOfRewritingTheBlock(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		"var edits = emptyEdits();",
		"function emptyEdits(){ return { top:{}, models:{}, keys:{ add:{}, del:{}, set:{}, rename:{} } }; }",
		"function buildPayload(base){",
		"{ allowNotFound:true }",
		"function saveWith(credential){",
		"payload.enabled = true;",
		"function removeKey(name){",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Keys the page never saw must survive: additions and removals are applied on
	// top of the array read from the host, not on top of a page-side copy.
	if !strings.Contains(document, "keys = keys.filter(function(item){ return !(item && edits.keys.del[item.name]); });") {
		t.Error("removals must be applied to the host key list")
	}
	if strings.Contains(document, "configLoaded") {
		t.Error("a full local configuration copy is no longer kept; edits are patches")
	}
}

// A key name is the handle every other view uses (quota rows, auth-file hints,
// the account projection), so a rename is a first-class edit: recorded on its
// own, checked against the names the host will actually end up with, and merged
// before additions.
func TestPageRenamesKeys(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`id="keyNameHint"`,
		`data-key-rename="`,
		"function keyNameTaken(name, ignore){",
		"function setKeyRename(original, raw){",
		"edits.keys.rename[original] = target;",
		"Object.keys(edits.keys.rename).forEach(function(name){",
		"keys[index[name]].name = edits.keys.rename[name];",
		"Key 名称不能为空",
		"已存在同名 Key：",
		"name + ' → ' + edits.keys.rename[name]",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}

	// Renames run after removals/per-key edits and before additions, and the
	// index is rebuilt in between, so "rename A to B, then add a new A" keeps
	// both keys instead of overwriting the renamed one.
	renameAt := strings.Index(document, "keys[index[name]].name = edits.keys.rename[name];")
	addAt := strings.Index(document, "keys.push({ name:name, key:added.secret, priority:added.priority, enabled:added.enabled });")
	if renameAt < 0 || addAt < 0 || renameAt > addAt {
		t.Fatal("buildPayload must apply renames before additions")
	}
	if !strings.Contains(document[renameAt:addAt], "index = {};") {
		t.Error("buildPayload must rebuild the key index after renames")
	}
}

// The page fills the iframe with a top action bar, a tab strip and one scrolling
// region. Nothing may be anchored to the bottom edge: on phones the management
// centre can leave a strip of its own background below the iframe.
func TestPageHasNoBottomAnchoredBar(t *testing.T) {
	document := page(t)
	if strings.Contains(document, "position:fixed") {
		t.Error("no element may be fixed-positioned; the page must not float over the parent's strip")
	}
	for _, want := range []string{"class=\"toolbar\"", "class=\"tabs\"", "class=\"panes\"", "--host-header", "flex-direction:column;height:100%"} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// The layout has to hold up on a phone: the tab strip scrolls, cards collapse to
// one column, and the password dialog becomes a bottom sheet.
func TestPageAdaptsToMobile(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"`,
		"@media (max-width:640px)",
		"@media (max-width:900px)",
		"overflow-x:auto;scrollbar-width:none",
		"env(safe-area-inset-bottom)",
		".key-quota{grid-template-columns:minmax(0,1fr)}",
		"border-radius:14px 14px 0 0",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing the mobile rule %q", want)
		}
	}
}

// Model settings stay collapsed behind a per-row button, and the row has to say
// which id clients actually call because an alias replaces the model id.
func TestPageModelControls(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`id="modelFilter"`,
		`data-model-settings="`,
		`data-model-alias="`,
		`data-model-family="`,
		`data-model-proxy="`,
		`data-model-probe="`,
		"opencode-go/probe?model=",
		"收起全部设置",
		`<option value="edited">本次已修改</option>`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Settings inputs are only rendered by the expandable panel.
	if !strings.Contains(document, "if(isOpen)") {
		t.Error("the settings panel must be rendered only when expanded")
	}
	if !strings.Contains(document, "row.client_ids") {
		t.Error("the page must show the client-facing ids of a renamed model")
	}
	if !strings.Contains(document, "row.aliased") {
		t.Error("the page must know whether an alias renamed the model")
	}
	if strings.Contains(document, "row.is_alias") || strings.Contains(document, "row.base_id") {
		t.Error("alias rows no longer exist; the page must not depend on them")
	}
}

// The statistics tab answers the operator's question per used model: tokens,
// list-price spend, share of the monthly allowance, average TPS and TTFT. On a
// phone the table is folded so the first screen is the summary, not a wall of
// numbers; the methodology is always folded.
func TestPageStatsSummarizesUsedModels(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		`id="tab-stats"`, `id="pane-stats"`, `id="statsSummary"`, `id="usageStatsTable"`,
		`id="statsModelsFold"`, `id="statsAboutFold"`, `id="statsRange"`, `id="refreshStats"`,
		`id="statsError"`, `id="statsWarnings"`, `id="statsPlanHint"`, `id="statsPlanBtn"`,
		`id="statsPriceRevision"`, `id="statsPriceCheckedAt"`, `id="statsPriceNotice"`,
		`id="statsPriceNoticeBadge"`, `id="statsPriceFreshness"`, `id="statsPriceFreshLine"`, `id="statsPriceLink"`, `id="cfgPlan"`,
		"/api/stats", "function loadUsageStats(force)", "function renderUsageTable()", "function usageRow(model)",
		"function renderPriceFreshness()", "price_book_update_due", "price_book_checked_at",
		"model_list_checked_at", "model_list_changed", "model_list_added", "model_list_removed",
		"function formatModelIDs(ids)", "上游模型列表与",
		"数据该更新了", "模型列表有变更", "模型列表无变化", "不代表上游已经调价", "30 天周期",
		"function statsSinceISO()", "function monthBefore(date)", "setUTCMonth(result.getUTCMonth() - 1)",
		"var statsCalendarRetry = false;", "stats.range.since_source === 'calendar'",
		`data-label="请求"`, `data-label="输入"`, `data-label="输出"`, `data-label="缓存读/写"`,
		`data-label="刊例价"`, `data-label="本月额度"`, `data-label="平均 TPS"`, `data-label="生成 TPS"`, `data-label="平均 TTFT"`,
		"成功请求的有效 output token ÷ 宿主请求总延迟", "不含 TTFT", "回放到本统计", "金额为刊例价估算，不是 OpenCode 账单",
		"matchMedia('(max-width: 640px)')", "fold.open = false", ".fold-block{", ".stats-table-wrap{", ".stats-freshness{", ".stats-freshness[hidden]{display:none}",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The three non-summary blocks are real details elements, not divs that
	// merely look collapsible.
	if !strings.Contains(document, `<details class="fold-block" id="statsModelsFold" open>`) {
		t.Error("the model table must live in a details element")
	}
	if !strings.Contains(document, `<details class="card fold-card" id="statsAboutFold">`) {
		t.Error("the methodology must live in a folded details card")
	}
	// Switching the plan from the statistics page jumps to the settings select.
	if !strings.Contains(document, "showTab('settings');") {
		t.Error("the plan shortcut must open the settings tab")
	}
}

func TestJSStringEscapesInjection(t *testing.T) {
	document := string(Page(`</SCRIPT><script>alert(1)</script>`, "chat"))
	if strings.Contains(document, "<script>alert(1)</script>") {
		t.Fatal("plugin ids must not be able to close the script tag")
	}
	if !strings.Contains(document, `\u003c/SCRIPT\u003e`) {
		t.Error("angle brackets must be escaped")
	}
}

// The page is one inline script: a single syntax error leaves the document
// rendered but dead (no tabs, no data), which is exactly what shipped in 1.8.0.
// Substring assertions cannot catch that, so run the real parser when node is
// available; a machine without node skips instead of failing.
func TestPageScriptParses(t *testing.T) {
	node, errLookPath := exec.LookPath("node")
	if errLookPath != nil {
		t.Skip("node is not installed; skipping the JavaScript syntax check")
	}
	document := page(t)
	script, ok := inlineScript(document)
	if !ok {
		t.Fatal("the page has no inline <script> block")
	}
	path := filepath.Join(t.TempDir(), "page.js")
	if errWrite := os.WriteFile(path, []byte(script), 0o600); errWrite != nil {
		t.Fatalf("write script: %v", errWrite)
	}
	output, errCheck := exec.Command(node, "--check", path).CombinedOutput()
	if errCheck != nil {
		t.Fatalf("the page script does not parse: %v\n%s", errCheck, output)
	}
}

// The rename path is the one piece of page logic that cannot be checked with
// substring assertions: it depends on the order of delete/set/rename/add in
// buildPayload. Run the real inline script in a Node VM with a DOM stub and a
// tiny test hook injected before the startup calls. A machine without node
// skips, like TestPageScriptParses.
func TestPageRenameLogic(t *testing.T) {
	node, errLookPath := exec.LookPath("node")
	if errLookPath != nil {
		t.Skip("node is not installed; skipping the JavaScript behaviour check")
	}
	document := page(t)
	script, ok := inlineScript(document)
	if !ok {
		t.Fatal("the page has no inline <script> block")
	}
	scriptPath := filepath.Join(t.TempDir(), "page.js")
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0o600); errWrite != nil {
		t.Fatalf("write script: %v", errWrite)
	}
	harness := filepath.Join("testdata", "rename_logic.js")
	output, errRun := exec.Command(node, harness, scriptPath).CombinedOutput()
	if errRun != nil {
		t.Fatalf("rename logic harness failed: %v\n%s", errRun, output)
	}
}

// inlineScript returns the body of the first inline <script> block.
func inlineScript(document string) (string, bool) {
	const open = "<script>"
	const close = "</script>"
	start := strings.Index(document, open)
	if start < 0 {
		return "", false
	}
	start += len(open)
	end := strings.Index(document[start:], close)
	if end < 0 {
		return "", false
	}
	return document[start : start+end], true
}

// The account page must explain that CPA's AuthManager owns scheduling and that
// the plugin only projects the keys into auth files.
func TestPageDocumentsCPAScheduling(t *testing.T) {
	document := page(t)
	for _, want := range []string{
		"CPA AuthManager",
		"CPA 认证文件",
		"managed_by: cpa-opencodego",
		"同步重命名对应的 CPA 认证文件",
		"插件自身不实现 Key 轮询",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	if strings.Contains(document, "插件不再向 CPA 投影认证文件") {
		t.Error("the page still claims that the auth projection was removed")
	}
}
