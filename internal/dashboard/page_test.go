package dashboard

import (
	"bytes"
	"strings"
	"testing"
)

func TestPageEmbedsPluginIDSafely(t *testing.T) {
	page := Page(`evil</script><script>alert(1)</script>`, "1.0.0")
	// The injected closing tag must not terminate the page's own script block.
	if bytes.Count(page, []byte("</script>")) != 1 {
		t.Fatalf("page has %d closing script tags, want exactly 1", bytes.Count(page, []byte("</script>")))
	}
	if !bytes.Contains(page, []byte(`\u003c/script\u003e`)) {
		t.Fatalf("plugin id was not escaped as a JS string: %s", snippetOf(page))
	}
}

func TestPageRendersExpectedSections(t *testing.T) {
	page := string(Page("cpa-opencodego-quota", "1.2.3"))
	for _, want := range []string{
		`var PLUGIN_ID = "cpa-opencodego-quota";`,
		"Key 额度",
		"路由分组",
		"刷新额度",
		"v1.2.3",
		"/api/quota",
		"OpenCode Go 额度",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
	if strings.Contains(page, "__PLUGIN_ID__") || strings.Contains(page, "__SCRIPT__") || strings.Contains(page, "__STYLES__") {
		t.Fatal("page still contains template placeholders")
	}
}

func TestPageMentionsReadOnlyContract(t *testing.T) {
	page := strings.ToLower(string(Page("cpa-opencodego-quota", "1.0.0")))
	if !strings.Contains(page, "只读") {
		t.Fatal("page does not state that the plugin is read-only")
	}
}

func snippetOf(page []byte) string {
	text := string(page)
	if len(text) > 240 {
		return text[:240]
	}
	return text
}
