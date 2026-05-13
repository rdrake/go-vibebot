package scene

import (
	"context"
	"strings"
	"testing"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/store"
)

type captureLLM struct {
	resp     string
	lastUser string
}

func (c *captureLLM) Complete(_ context.Context, req llm.CompleteRequest) (string, error) {
	if len(req.Messages) > 0 {
		c.lastUser = req.Messages[0].Content
	}
	return c.resp, nil
}

func (*captureLLM) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func TestSynthesizePrependsLeaderMemory(t *testing.T) {
	mem := memory.NewInMem(0)
	past := store.NewInjectEvent("s1", "", "the lads found a stray cat last week")
	past.ID = store.EventID(1)
	_ = mem.Record(context.Background(), past)

	leader := mkChar("archie", "Archie")
	leader.Persona = "leader of the lads"
	leader.Memory = mem

	s := &Scene{Leader: leader}
	cap := &captureLLM{resp: "ok"}
	cur := store.NewInjectEvent("s1", "", "a new cat appears")
	cur.ID = store.EventID(2)

	_, err := s.synthesize(context.Background(), cap, cur, "a new cat appears", []string{"sam: cool"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.lastUser, "Group's recent history:") {
		t.Fatalf("user msg missing memory prelude:\n%s", cap.lastUser)
	}
	if !strings.Contains(cap.lastUser, "stray cat last week") {
		t.Fatalf("user msg missing past event text:\n%s", cap.lastUser)
	}
}

func TestSynthesizeSkipsCurrentEventInRecall(t *testing.T) {
	mem := memory.NewInMem(0)
	cur := store.NewInjectEvent("s1", "", "a new cat appears")
	cur.ID = store.EventID(2)
	_ = mem.Record(context.Background(), cur)

	leader := mkChar("archie", "Archie")
	leader.Memory = mem

	s := &Scene{Leader: leader}
	cap := &captureLLM{resp: "ok"}

	_, err := s.synthesize(context.Background(), cap, cur, "a new cat appears", []string{"sam: cool"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cap.lastUser, "Group's recent history:") {
		t.Fatalf("prelude should be omitted when retrieval only returns the current event:\n%s", cap.lastUser)
	}
}

func TestSynthesizeNoMemoryRendersWithoutPrelude(t *testing.T) {
	leader := mkChar("archie", "Archie")
	leader.Memory = memory.NewInMem(0)

	s := &Scene{Leader: leader}
	cap := &captureLLM{resp: "ok"}
	cur := store.NewInjectEvent("s1", "", "a new cat appears")
	cur.ID = store.EventID(2)

	_, err := s.synthesize(context.Background(), cap, cur, "a new cat appears", []string{"sam: cool"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cap.lastUser, "Group's recent history:") {
		t.Fatalf("empty memory should not produce prelude:\n%s", cap.lastUser)
	}
	if !strings.Contains(cap.lastUser, "Situation:") {
		t.Fatalf("expected Situation: in user msg:\n%s", cap.lastUser)
	}
}

func TestSynthesizeNoLeaderReturnsJoinedReplies(t *testing.T) {
	s := &Scene{}
	cap := &captureLLM{resp: "should-not-be-called"}
	out, err := s.synthesize(context.Background(), cap, store.Event{}, "p", []string{"a: x", "b: y"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "a: x | b: y" {
		t.Errorf("got %q want %q", out, "a: x | b: y")
	}
	if cap.lastUser != "" {
		t.Error("LLM should not be called when no leader")
	}
}
