package models

import (
	"testing"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
)

// The id-prefix rule is what routes a model OpenCode Go publishes after this
// build was compiled. It must agree with the built-in table for every known
// model, otherwise a catalogue entry and the inference would disagree.
func TestFamilyHintMatchesCatalogue(t *testing.T) {
	known := Known()
	if len(known) == 0 {
		t.Fatal("catalogue is empty")
	}
	for _, model := range known {
		if hint := FamilyHint(model.ID); hint != model.Family {
			t.Errorf("FamilyHint(%q) = %q, catalogue says %q", model.ID, hint, model.Family)
		}
	}
}

func TestFamilyHintUnknownIDs(t *testing.T) {
	cases := map[string]roles.Name{
		"claude-opus-9":              roles.Messages,
		"gemini-5-boron-contributor": roles.Gemini,
		"gpt-7-orbit":                roles.Responses,
		"grok-5":                     roles.Responses,
		"muse-spark-2.0":             roles.Responses,
		"glm-6":                      roles.Chat,
		"some-new-vendor-model":      roles.Chat,
		"":                           roles.Chat,
		"  CLAUDE-Sonnet-9  ":        roles.Messages,
	}
	for id, want := range cases {
		if got := FamilyHint(id); got != want {
			t.Errorf("FamilyHint(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestFamilyForPrefersCatalogue(t *testing.T) {
	// A catalogue id always reports the documented endpoint, even when the
	// prefix rule would guess the same thing (that is what the test above
	// guarantees) and regardless of casing.
	if got := FamilyFor("grok-4.7"); got != roles.Responses {
		t.Errorf("FamilyFor(grok-4.7) = %q", got)
	}
	if got := FamilyFor("gpt-9-unknown"); got != roles.Responses {
		t.Errorf("FamilyFor(unknown) = %q, want the prefix hint", got)
	}
}

func TestIsCatalogued(t *testing.T) {
	if !IsCatalogued("deepseek-v4-pro") {
		t.Error("deepseek-v4-pro should be catalogued")
	}
	if IsCatalogued("deepseek-v9-imaginary") {
		t.Error("an unknown id must not report as catalogued")
	}
	if IsCatalogued("  ") {
		t.Error("a blank id must not report as catalogued")
	}
}

// OpenCode Go renamed Gemini 4 Argon: the contributor suffix is gone and the
// old id answers 410 ModelDeprecated, so only the new id may be advertised.
func TestGeminiArgonUsesRenamedUpstreamID(t *testing.T) {
	entry, ok := catalog["gemini-4-argon"]
	if !ok {
		t.Fatal("gemini-4-argon missing from the catalogue")
	}
	if entry.Family != roles.Gemini {
		t.Errorf("gemini-4-argon family = %q, want gemini", entry.Family)
	}
	if IsCatalogued("gemini-4-argon-contributor") {
		t.Error("the deprecated contributor id must not be advertised")
	}
	if !IsCatalogued("gemini-4-argon") {
		t.Error("gemini-4-argon should be catalogued")
	}
}
