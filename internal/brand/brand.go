// Package brand holds the display identity the plugin publishes to the host and
// renders on its own read-only page.
//
// The plugin is one small product: a quota viewer for the OpenCode Go keys that
// live in the host configuration. It deliberately carries no executor, no model
// catalogue and no credentials of its own, so the brand is only a name, a
// repository link and the upstream mark that identifies what the keys belong to.
package brand

import (
	_ "embed"
	"encoding/base64"
	"strings"
)

// embeddedMarkup is the OpenCode square icon from https://opencode.ai/favicon.svg
// with the redundant wrapper and the unused prefers-color-scheme style block
// dropped. OpenCode is a project of Anomaly (MIT); the mark is used here
// descriptively to identify the upstream service the keys are issued by, and is
// not covered by this repository's license.
//
//go:embed opencode.svg
var embeddedMarkup string

// Markup is the mark without surrounding whitespace, so it can be inlined into
// the page document exactly as embedded.
var Markup = strings.TrimSpace(embeddedMarkup)

// Logo is the value handed to the host as plugin metadata. The management
// centre resolves metadata logos through an allow-list that accepts http(s),
// data: and blob: sources; a data: URI keeps the icon working without a network
// round trip and without shipping a separate asset route.
var Logo = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(Markup))

// Product identity.
const (
	// Name is the plugin name shown in the management centre's plugin list and
	// as the sidebar entry of the quota page.
	Name = "OpenCode Go 额度"
	// Author identifies the plugin author or organization.
	Author = "cpa-opencodego"
	// Repository is the repository URL for source and support.
	Repository = "https://github.com/Pheobe-Southwood/cpa-opencodego"
	// Attribution credits the upstream project whose mark is embedded above.
	Attribution = "OpenCode icon © 2025 opencode (https://opencode.ai, MIT)"
)
