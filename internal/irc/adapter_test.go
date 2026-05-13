package irc

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/lrstanley/girc"
)

func TestRetryableConnectErrorTreatsPermanentErrorsAsFatal(t *testing.T) {
	if retryableConnectError(&girc.InvalidConfigError{}) {
		t.Fatal("invalid girc config should not be retried")
	}
	if retryableConnectError(&girc.EventError{Event: &girc.Event{Params: []string{"closing connection: SASL authentication failed"}}}) {
		t.Fatal("SASL authentication failure should not be retried")
	}
}

func TestRetryableConnectErrorAllowsTransientNetworkErrors(t *testing.T) {
	if !retryableConnectError(&net.DNSError{Err: "no such host", Name: "irc.example.invalid"}) {
		t.Fatal("DNS failure should be retried")
	}
	if !retryableConnectError(errors.New("read: connection reset by peer")) {
		t.Fatal("generic connection loss should be retried")
	}
}

func TestCmdLogFiltersAmbientByDefault(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{
		{Timestamp: time.Date(2026, 5, 12, 19, 12, 0, 0, time.UTC), Actor: "world", Kind: "ambient", Text: "time passes"},
		{Timestamp: time.Date(2026, 5, 12, 19, 13, 0, 0, time.UTC), Actor: "assgas-archie", Kind: "synthesized", Text: "Stinky Evening begins."},
	}}
	a, err := New(Config{Server: "irc.example", Channel: "#c", Nick: "bot"}, fw)
	if err != nil {
		t.Fatal(err)
	}
	var replies []string
	a.cmdLog(context.Background(), "1h", func(s string) { replies = append(replies, s) })

	if len(fw.LogCalls) != 1 || fw.LogCalls[0] != time.Hour {
		t.Fatalf("LogCalls=%+v", fw.LogCalls)
	}
	got := strings.Join(replies, "\n")
	if strings.Contains(got, "time passes") {
		t.Fatalf("ambient tick should be hidden by default: %q", got)
	}
	if !strings.Contains(got, "Stinky Evening begins.") {
		t.Fatalf("non-ambient event missing: %q", got)
	}
}

func TestCmdLogCanIncludeAmbient(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{
		{Timestamp: time.Date(2026, 5, 12, 19, 12, 0, 0, time.UTC), Actor: "world", Kind: "ambient", Text: "time passes"},
	}}
	a, err := New(Config{Server: "irc.example", Channel: "#c", Nick: "bot"}, fw)
	if err != nil {
		t.Fatal(err)
	}
	var replies []string
	a.cmdLog(context.Background(), "--ambient 30m", func(s string) { replies = append(replies, s) })

	if len(fw.LogCalls) != 1 || fw.LogCalls[0] != 30*time.Minute {
		t.Fatalf("LogCalls=%+v", fw.LogCalls)
	}
	got := strings.Join(replies, "\n")
	if !strings.Contains(got, "time passes") {
		t.Fatalf("ambient tick should be included: %q", got)
	}
}

func TestCmdSnapshotSummarizesCharactersAndPlaces(t *testing.T) {
	fw := &fakeWorld{
		CharactersReturn: []api.CharacterRef{
			{ID: "assgas-archie", Name: "Assgas Archie"},
			{ID: "diarrhea-dan", Name: "Diarrhoea Dan"},
			{ID: "vicar", Name: "The Vicar"},
		},
		PlacesReturn: []api.PlaceRef{
			{
				ID:      "eton-on-thames",
				SceneID: "place:eton-on-thames",
				Leader:  "headmaster-mcweevil",
				Members: []api.CharacterRef{{ID: "headmaster-mcweevil"}, {ID: "assgas-archie"}},
			},
			{
				ID:      "cathedral",
				SceneID: "place:cathedral",
				Leader:  "vicar",
				Members: []api.CharacterRef{{ID: "vicar"}},
			},
		},
	}
	a, err := New(Config{Server: "irc.example", Channel: "#c", Nick: "bot"}, fw)
	if err != nil {
		t.Fatal(err)
	}
	var replies []string
	a.cmdSnapshot(context.Background(), func(s string) { replies = append(replies, s) })

	got := strings.Join(replies, "\n")
	for _, want := range []string{
		"characters: 3",
		"places: 2",
		"eton-on-thames scene=place:eton-on-thames leader=headmaster-mcweevil members=2",
		"cathedral scene=place:cathedral leader=vicar members=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("snapshot missing %q in %q", want, got)
		}
	}
}
