package irc

import "testing"

func TestParseInjectArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        string
		wantScene   string
		wantDesc    string
		wantOK      bool
	}{
		{"empty", "", "", "", false},
		{"plain", "found a sandwich", "", "found a sandwich", true},
		{"scene only", "@cathedral", "", "", false},
		{"scene and desc", "@cathedral the floor smells of incense", "cathedral", "the floor smells of incense", true},
		{"scene with namespace colon", "@place:cathedral incense again", "place:cathedral", "incense again", true},
		{"extra whitespace", "@cathedral    candle falls   ", "cathedral", "candle falls", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sceneID, desc, ok := parseInjectArgs(tt.args)
			if ok != tt.wantOK {
				t.Fatalf("ok: want %v, got %v", tt.wantOK, ok)
			}
			if !ok {
				return
			}
			if string(sceneID) != tt.wantScene {
				t.Errorf("scene: want %q, got %q", tt.wantScene, sceneID)
			}
			if desc != tt.wantDesc {
				t.Errorf("desc: want %q, got %q", tt.wantDesc, desc)
			}
		})
	}
}

func TestParseSummonArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantPlace string
		wantNPCs  []string
		wantDesc  string
		wantErr   bool
	}{
		{"legacy", "cathedral", "cathedral", nil, "", false},
		{"adhoc full", "spire n=vicar,booger-bertha A drafty steeple.", "spire", []string{"vicar", "booger-bertha"}, "A drafty steeple.", false},
		{"adhoc no desc", "spire n=vicar", "spire", []string{"vicar"}, "", false},
		{"empty npc entry", "spire n=vicar,,bertha", "", nil, "", true},
		{"empty npc list", "spire n=", "", nil, "", true},
		{"legacy with trailing", "cathedral some description", "", nil, "", true},
		{"n= after second token", "tavern A dark night n=bertha", "", nil, "", true},
		{"whitespace", "  spire  n=vicar  desc  ", "spire", []string{"vicar"}, "desc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			placeID, npcs, desc, err := parseSummonArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err: want %v, got %v", tt.wantErr, err)
			}
			if tt.wantErr {
				return
			}
			if string(placeID) != tt.wantPlace {
				t.Errorf("placeID: want %q, got %q", tt.wantPlace, placeID)
			}
			gotIDs := make([]string, len(npcs))
			for i, n := range npcs {
				gotIDs[i] = string(n)
			}
			if !equalStringSlices(gotIDs, tt.wantNPCs) {
				t.Errorf("npcs: want %v, got %v", tt.wantNPCs, gotIDs)
			}
			if desc != tt.wantDesc {
				t.Errorf("desc: want %q, got %q", tt.wantDesc, desc)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
