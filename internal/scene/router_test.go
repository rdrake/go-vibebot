package scene

import (
	"context"
	"errors"
	"testing"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

type fixedLLM struct {
	resp string
	err  error
}

func (f fixedLLM) Complete(_ context.Context, _ llm.CompleteRequest) (string, error) {
	return f.resp, f.err
}
func (fixedLLM) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func TestAllRouterReturnsAllCandidates(t *testing.T) {
	cs := []*character.Character{mkChar("a", "A"), mkChar("b", "B")}
	got, err := AllRouter{}.Select(context.Background(), store.Event{}, nil, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("want 2, got %d", len(got))
	}
}

func TestLLMRouterPicksFromResponse(t *testing.T) {
	cs := []*character.Character{
		mkChar("alpha", "Alpha", "sandwich"),
		mkChar("beta", "Beta", "sandwich"),
		mkChar("gamma", "Gamma", "sandwich"),
	}
	ev := store.NewInjectEvent("s", "x", "sandwich emergency")
	r := LLMRouter{Model: fixedLLM{resp: "alpha, gamma"}}
	got, err := r.Select(context.Background(), ev, nil, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "gamma" {
		t.Errorf("got=%v want [alpha gamma]", ids(got))
	}
}

func TestLLMRouterFallsBackToPrefilterOnGarbage(t *testing.T) {
	cs := []*character.Character{
		mkChar("alpha", "Alpha", "sandwich"),
		mkChar("beta", "Beta", "sandwich"),
	}
	ev := store.NewInjectEvent("s", "x", "sandwich emergency")
	r := LLMRouter{Model: fixedLLM{resp: "I have no idea who should react."}}
	got, err := r.Select(context.Background(), ev, nil, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("want prefilter fallback (both), got %v", ids(got))
	}
}

func TestLLMRouterFallsBackOnLLMError(t *testing.T) {
	cs := []*character.Character{mkChar("a", "A", "x"), mkChar("b", "B", "x")}
	ev := store.NewInjectEvent("s", "x", "x event")
	r := LLMRouter{Model: fixedLLM{err: errors.New("offline")}}
	got, err := r.Select(context.Background(), ev, nil, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("want non-empty fallback")
	}
}

func TestLLMRouterMaxConsultCap(t *testing.T) {
	cs := []*character.Character{
		mkChar("a", "A", "x"), mkChar("b", "B", "x"), mkChar("c", "C", "x"),
	}
	ev := store.NewInjectEvent("s", "x", "x x x")
	r := LLMRouter{Model: fixedLLM{resp: "a,b,c"}, MaxConsult: 2}
	got, _ := r.Select(context.Background(), ev, nil, cs)
	if len(got) != 2 {
		t.Errorf("want capped at 2, got %d", len(got))
	}
}

func TestLLMRouterSinglePrefilteredSkipsLLM(t *testing.T) {
	cs := []*character.Character{
		mkChar("only-match", "OnlyMatch", "sandwich"),
		mkChar("unrelated", "Unrelated", "stoic"),
	}
	ev := store.NewInjectEvent("s", "x", "sandwich")
	r := LLMRouter{Model: fixedLLM{resp: "unrelated"}} // would mis-pick if asked
	got, _ := r.Select(context.Background(), ev, nil, cs)
	if len(got) != 1 || got[0].ID != "only-match" {
		t.Errorf("want only-match, got %v", ids(got))
	}
}

func TestPickByResponseHandlesQuotesAndNewlines(t *testing.T) {
	valid := map[api.CharacterID]*character.Character{
		"alpha": {ID: "alpha"}, "beta": {ID: "beta"},
	}
	got := pickByResponse(`"alpha"`+"\n"+`'beta'`, valid, nil)
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "beta" {
		t.Errorf("got %v want [alpha beta]", ids(got))
	}
}
