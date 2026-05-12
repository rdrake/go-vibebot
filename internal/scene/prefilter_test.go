package scene

import (
	"testing"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
)

func mkChar(id api.CharacterID, name string, caps ...string) *character.Character {
	return &character.Character{ID: id, Name: name, Capabilities: caps}
}

func ids(cs []*character.Character) []api.CharacterID {
	out := make([]api.CharacterID, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func TestPreFilterRanksByOverlap(t *testing.T) {
	cs := []*character.Character{
		mkChar("cook", "Cook", "cooking"),
		mkChar("snacker", "Snacker", "snacks", "persuasion"),
		mkChar("planner", "Planner", "logistics", "timing"),
	}
	got := PreFilter("found a suspicious sandwich, who wants snacks?", cs, 0)
	if len(got) == 0 {
		t.Fatal("expected at least one")
	}
	if got[0].ID != "snacker" {
		t.Errorf("want snacker first, got %s; full=%v", got[0].ID, ids(got))
	}
}

func TestPreFilterFallbackWhenNoOverlap(t *testing.T) {
	cs := []*character.Character{
		mkChar("a", "Alpha", "x"),
		mkChar("b", "Beta", "y"),
	}
	got := PreFilter("completely unrelated text", cs, 0)
	if len(got) != 2 {
		t.Fatalf("want all 2 returned on zero overlap, got %d", len(got))
	}
}

func TestPreFilterRespectsTopK(t *testing.T) {
	cs := []*character.Character{
		mkChar("a", "Alpha", "sandwich"),
		mkChar("b", "Beta", "sandwich"),
		mkChar("c", "Gamma", "sandwich"),
	}
	got := PreFilter("sandwich emergency", cs, 2)
	if len(got) != 2 {
		t.Fatalf("want top-2, got %d", len(got))
	}
}

func TestPreFilterStemMatchesViaSubstring(t *testing.T) {
	cs := []*character.Character{
		mkChar("p", "Panicker", "panicking"),
		mkChar("c", "Calm", "stoic"),
	}
	got := PreFilter("a sudden panic erupts", cs, 0)
	if len(got) == 0 || got[0].ID != "p" {
		t.Errorf("want Panicker first via panic⊂panicking, got %v", ids(got))
	}
}

func TestPreFilterEmptyInputs(t *testing.T) {
	if got := PreFilter("anything", nil, 0); got != nil {
		t.Errorf("want nil for nil input, got %v", got)
	}
	cs := []*character.Character{mkChar("a", "A", "x")}
	if got := PreFilter("", cs, 0); len(got) != 1 {
		t.Errorf("want passthrough on empty text, got %v", ids(got))
	}
}
