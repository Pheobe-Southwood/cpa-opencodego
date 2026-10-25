// Package models holds the OpenCode Go model catalogue.
//
// The catalogue is the mapping from a model id to the upstream protocol family
// that serves it, taken from https://opencode.ai/docs/go/ and verified against
// the live endpoint. The id list itself is refreshed at runtime from
// GET {base}/models so newly published models show up without a rebuild.
package models

import (
	"sort"
	"strings"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

// Model is one catalogue entry.
type Model struct {
	// ID is the upstream model id, for example "grok-4.7".
	ID string
	// Family is the upstream protocol family that serves this model.
	Family roles.Name
	// DisplayName is the user-facing label.
	DisplayName string
	// ContextLength is the upstream context window when known.
	ContextLength int64
	// Note carries a short advisory, for example the data-training caveat.
	Note string
}

// FamilySource explains where a model's protocol family came from. It lives
// here, next to the family tables, because both the configuration package and
// the catalogue package need it and neither may import the other.
type FamilySource string

const (
	// FamilyFromConfig is an explicit per-model family in the operator config.
	FamilyFromConfig FamilySource = "config"
	// FamilyFromCatalogue is the built-in id-to-endpoint table.
	FamilyFromCatalogue FamilySource = "catalogue"
	// FamilyFromHint is the id-prefix inference used for ids the built-in
	// catalogue does not know yet.
	FamilyFromHint FamilySource = "inferred"
)

// familyHints maps a model-id prefix to the upstream endpoint that serves it.
// The OpenCode Go model list grows over time, so an id that is not in the
// built-in catalogue is still routed to the right endpoint instead of always
// falling back to chat. catalog_test.go proves the table reproduces every
// catalogue entry, which keeps the two definitions honest.
var familyHints = []struct {
	prefix string
	family roles.Name
}{
	{prefix: "claude-", family: roles.Messages},
	{prefix: "gemini-", family: roles.Gemini},
	{prefix: "gpt-", family: roles.Responses},
	{prefix: "grok-", family: roles.Responses},
	{prefix: "muse-", family: roles.Responses},
}

// FamilyHint infers the protocol family from the model id alone. It is used for
// ids the catalogue does not know yet; the operator can always override the
// guess per model and the dashboard marks such models as inferred.
func FamilyHint(id string) roles.Name {
	lowered := strings.ToLower(strings.TrimSpace(id))
	for _, hint := range familyHints {
		if strings.HasPrefix(lowered, hint.prefix) {
			return hint.family
		}
	}
	return roles.Chat
}

// FamilyFor returns the protocol family that serves a model id: the built-in
// catalogue when the id is known, the prefix inference otherwise.
func FamilyFor(id string) roles.Name {
	if entry, ok := catalog[strings.TrimSpace(id)]; ok {
		return entry.Family
	}
	return FamilyHint(id)
}

// IsCatalogued reports whether the id is part of the built-in catalogue.
func IsCatalogued(id string) bool {
	_, ok := catalog[strings.TrimSpace(id)]
	return ok
}

// Known lists every catalogue entry, sorted by id.
func Known() []Model {
	out := make([]Model, 0, len(catalog))
	for _, entry := range catalog {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// chatModels are the models served by POST {base}/chat/completions.
var chatModels = []Model{
	{ID: "beam-free", DisplayName: "Beam Free"},
	{ID: "cat-jam-free", DisplayName: "CatJam (free)"},
	{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro"},
	{ID: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash"},
	{ID: "glm-5.1", DisplayName: "GLM 5.1"},
	{ID: "glm-5.2", DisplayName: "GLM 5.2"},
	{ID: "glm-5.3", DisplayName: "GLM 5.3"},
	{ID: "glm-5.3-flash", DisplayName: "GLM 5.3 Flash"},
	{ID: "hy3", DisplayName: "Hy3"},
	{ID: "hy4-preview", DisplayName: "Hy4 Preview"},
	{ID: "kimi-k2.6", DisplayName: "Kimi K2.6"},
	{ID: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code"},
	{ID: "kimi-k3", DisplayName: "Kimi K3"},
	{ID: "longcat-2.0", DisplayName: "LongCat 2.0"},
	{ID: "longcat-2.5-preview-free", DisplayName: "LongCat 2.5 Preview (free)"},
	{ID: "mimo-v2.5", DisplayName: "MiMo V2.5"},
	{ID: "mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro"},
	{ID: "mimo-v2.6-flash", DisplayName: "MiMo V2.6 Flash"},
	{ID: "mimo-v2.6-pro", DisplayName: "MiMo V2.6 Pro"},
	{ID: "minimax-m2.5", DisplayName: "MiniMax M2.5"},
	{ID: "minimax-m2.7", DisplayName: "MiniMax M2.7"},
	{ID: "minimax-m3", DisplayName: "MiniMax M3"},
	{ID: "omen-alpha", DisplayName: "Omen Alpha"},
	{ID: "qwen3.6-plus", DisplayName: "Qwen3.6 Plus"},
	{ID: "qwen3.7-max", DisplayName: "Qwen3.7 Max"},
	{ID: "qwen3.7-plus", DisplayName: "Qwen3.7 Plus"},
	{ID: "qwen3.8-flash", DisplayName: "Qwen3.8 Flash"},
	{ID: "qwen3.8-max", DisplayName: "Qwen3.8 Max"},
	{ID: "space-bunny", DisplayName: "Space Bunny"},
	{ID: "step-5-preview", DisplayName: "Step 5 Preview"},
}

// responsesModels are the models served by POST {base}/responses.
var responsesModels = []Model{
	{ID: "gpt-6-luna", DisplayName: "GPT 6 Luna", ContextLength: 400000},
	{ID: "gpt-5.6-luna", DisplayName: "GPT 5.6 Luna", ContextLength: 400000},
	{ID: "grok-4.7", DisplayName: "Grok 4.7", ContextLength: 256000},
	{ID: "grok-4.6", DisplayName: "Grok 4.6", ContextLength: 256000},
	{ID: "muse-spark-1.3-contributor", DisplayName: "Muse Spark 1.3 Contributor"},
	{ID: "muse-spark-1.2-contributor", DisplayName: "Muse Spark 1.2 Contributor"},
}

// messagesModels are the models served by POST {base}/messages.
var messagesModels = []Model{
	{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", ContextLength: 200000},
}

// geminiModels are the models served by the GenAI generateContent endpoint.
var geminiModels = []Model{
	{
		// OpenCode Go renamed this model: the old
		// "gemini-4-argon-contributor" id now answers 410 ModelDeprecated.
		ID:            "gemini-4-argon",
		DisplayName:   "Gemini 4 Argon",
		ContextLength: 1000000,
		Note:          "上游按请求数据训练且非 ZDR，请勿投喂敏感内容。",
	},
}

var catalog = buildCatalog()

func buildCatalog() map[string]Model {
	out := make(map[string]Model, 64)
	add := func(family roles.Name, entries []Model) {
		for _, entry := range entries {
			entry.Family = family
			if entry.DisplayName == "" {
				entry.DisplayName = entry.ID
			}
			out[entry.ID] = entry
		}
	}
	add(roles.Chat, chatModels)
	add(roles.Responses, responsesModels)
	add(roles.Messages, messagesModels)
	add(roles.Gemini, geminiModels)
	return out
}
