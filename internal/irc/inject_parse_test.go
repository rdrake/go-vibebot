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
