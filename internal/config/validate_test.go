package config

import (
	"strings"
	"testing"
)

func TestValidateOK(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "b"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a", "b"}}},
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
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `group "g" duplicate member "b"`) {
		t.Fatalf("missing duplicate member error: %v", err)
	}
}
