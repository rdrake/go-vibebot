package mcp

import (
	"testing"
)

func TestNewRejectsNilWorld(t *testing.T) {
	_, err := New(Config{}, nil)
	if err == nil {
		t.Fatal("New(nil) returned no error")
	}
}

func TestNewBuildsAdapterWithDefaults(t *testing.T) {
	a, err := New(Config{}, &fakeWorld{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Error("logger fallback not applied")
	}
	if a.server == nil {
		t.Error("server not constructed")
	}
}
