package irc

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		in       string
		wantOK   bool
		wantVerb string
		wantArgs string
	}{
		{"!inject Stinky Sam finds a sandwich", true, "inject", "Stinky Sam finds a sandwich"},
		{"!log 15m", true, "log", "15m"},
		{"!log", true, "log", ""},
		{"  !LOG  2h  ", true, "log", "2h"},
		{"hello world", false, "", ""},
		{"!", false, "", ""},
		{"", false, "", ""},
		{"!nudge stinky-sam", true, "nudge", "stinky-sam"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseCommand(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Verb != tt.wantVerb {
				t.Errorf("verb=%q want %q", got.Verb, tt.wantVerb)
			}
			if got.Args != tt.wantArgs {
				t.Errorf("args=%q want %q", got.Args, tt.wantArgs)
			}
		})
	}
}
