package config

import (
	"strings"
	"testing"
)

func TestValidateOK(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "b"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a", "b"}}},
		nil,
	)
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidateCatchesEveryProblem(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "a"}, {ID: ""}},
		[]GroupSpec{
			{ID: "", Members: []string{"ghost"}},
			{ID: "g2", Leader: "missing", Members: []string{"a", "missing-member"}},
			{ID: "g3", Leader: "a", Members: []string{}},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"duplicate character id",
		"empty id",
		"member \"ghost\"",
		"leader \"missing\"",
		"member \"missing-member\"",
		"leader \"a\" not in members",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in error: %s", want, msg)
		}
	}
}

func TestValidateRejectsDuplicateGroupMembers(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "b"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a", "b", "b"}}},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `group "g" duplicate member "b"`) {
		t.Fatalf("missing duplicate member error: %v", err)
	}
}

func TestValidatePlaceNPCsMustExist(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "b"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a"}}},
		[]PlaceSpec{
			{ID: "cathedral", NPCs: []string{"a", "ghost"}},
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `place "cathedral" npc "ghost" not in characters`) {
		t.Fatalf("missing npc-not-in-characters error: %v", err)
	}
}

func TestValidateRejectsDuplicateAndEmptyPlaceIDs(t *testing.T) {
	err := Validate(
		nil,
		nil,
		[]PlaceSpec{
			{ID: ""},
			{ID: "x"},
			{ID: "x"},
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"place with empty id",
		`duplicate place id "x"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in error: %s", want, msg)
		}
	}
}

func TestValidatePlaceWithNoNPCsWarnsButNotError(t *testing.T) {
	// Empty NPC list is allowed by Validate (a "place without people" is
	// data the caller may filter out later). It must not error here.
	err := Validate(
		[]CharacterSpec{{ID: "a"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a"}}},
		[]PlaceSpec{{ID: "empty"}},
	)
	if err != nil {
		t.Fatalf("place with empty NPCs should not error, got %v", err)
	}
}
