// Package dashboard renders the plugin's read-only quota page.
//
// The document is served from an unauthenticated resource route, so it renders
// nothing secret: every key is masked and the page's only network call is the
// plugin's own credential-free /api/quota route. The management centre embeds
// the page in an iframe under its fixed header, so the document mirrors the
// parent's theme and measures the parent's header before laying itself out.
package dashboard

import (
	"strings"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/brand"
)

// Page returns the quota page for a plugin id and build version.
func Page(pluginID, version string) []byte {
	// The script is expanded first: strings.Replacer does not rescan the text it
	// inserted, so a placeholder inside the injected script would survive.
	script := strings.ReplaceAll(pageScript, "__PLUGIN_ID__", jsString(pluginID))
	replacer := strings.NewReplacer(
		"__PLUGIN_ID__", jsString(pluginID),
		"__VERSION__", htmlText(version),
		"__NAME__", htmlText(brand.Name),
		"__REPO_URL__", htmlAttribute(brand.Repository),
		"__ATTRIBUTION__", htmlText(brand.Attribution),
		"__MARK__", brand.Markup,
		"__STYLES__", pageStyles,
		"__SCRIPT__", script,
	)
	return []byte(replacer.Replace(pageTemplate))
}

// jsString escapes a value for embedding inside a JavaScript string literal.
func jsString(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"<", `\u003c`,
		">", `\u003e`,
		"&", `\u0026`,
		"\u2028", `\u2028`,
		"\u2029", `\u2029`,
	)
	return replacer.Replace(value)
}

// htmlText escapes a value for embedding as HTML text.
func htmlText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// htmlAttribute escapes a value for embedding inside a double-quoted attribute.
func htmlAttribute(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

const pageTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>__NAME__</title>
<style>__STYLES__</style>
</head>
<body>
<div class="page">
  <header class="bar">
    <div class="brand">
      <span class="mark" aria-hidden="true">__MARK__</span>
      <div>
        <h1>__NAME__</h1>
        <p class="sub" id="config-path">正在读取宿主配置…</p>
      </div>
    </div>
    <div class="actions">
      <button type="button" class="btn primary" id="refresh">刷新额度</button>
      <label class="toggle"><input type="checkbox" id="auto" checked> 自动刷新</label>
    </div>
  </header>

  <section class="banner hidden" id="banner"></section>

  <section class="chips" id="summary"></section>

  <section class="panel">
    <div class="panel-head">
      <h2>Key 额度</h2>
      <span class="meta" id="meta"></span>
    </div>
    <div class="grid" id="keys"><div class="empty">加载中…</div></div>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>路由分组</h2>
      <span class="meta">宿主 config.yaml 中 base-url 指向 OpenCode Go 的 api-keys 分组</span>
    </div>
    <div class="groups">
      <table>
        <thead><tr><th>分组</th><th>协议</th><th>Base URL</th><th>额度探测出口</th><th>优先级</th><th>模型</th></tr></thead>
        <tbody id="groups-body"><tr><td colspan="6" class="tag-muted">加载中…</td></tr></tbody>
      </table>
    </div>
  </section>

  <footer class="foot">
    只读插件：读取宿主 config.yaml 与 OpenCode Go <code>/usage</code>，不写入任何配置 · v__VERSION__ ·
    <a href="__REPO_URL__" target="_blank" rel="noreferrer">源码</a> · __ATTRIBUTION__
  </footer>
</div>
<script>__SCRIPT__</script>
</body>
</html>
`
