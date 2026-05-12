package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlacesReadsEveryYAML(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", "id: a\nname: A\ndescription: first\nnpcs: [x]\n")
	write("b.yaml", "id: b\nname: B\ndescription: second\nnpcs: [y, z]\n")
	// non-yaml file should be ignored
	write("README.txt", "not yaml")

	places, err := LoadPlaces(dir)
	if err != nil {
		t.Fatalf("LoadPlaces: %v", err)
	}
	if len(places) != 2 {
		t.Fatalf("want 2 places, got %d", len(places))
	}
	// Sorted by id for deterministic test/iteration order.
	if places[0].ID != "a" || places[1].ID != "b" {
		t.Fatalf("want sorted [a, b], got [%s, %s]", places[0].ID, places[1].ID)
	}
	if len(places[1].NPCs) != 2 || places[1].NPCs[0] != "y" {
		t.Fatalf("npc list not parsed: %+v", places[1].NPCs)
	}
}

func TestLoadPlacesMissingDirReturnsEmpty(t *testing.T) {
	places, err := LoadPlaces(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if len(places) != 0 {
		t.Fatalf("want empty, got %d", len(places))
	}
}

func TestLoadPlacesPropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("not: : valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlaces(dir); err == nil {
		t.Fatal("expected parse error")
	}
}
