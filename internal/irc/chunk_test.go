package irc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkForConcatShortInput(t *testing.T) {
	got := chunkForConcat("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("want single passthrough, got %v", got)
	}
}

func TestChunkForConcatEmptyAndWhitespace(t *testing.T) {
	if got := chunkForConcat("", 100); got != nil {
		t.Errorf("want nil for empty, got %v", got)
	}
	if got := chunkForConcat("   \t  ", 100); got != nil {
		t.Errorf("want nil for whitespace-only, got %v", got)
	}
}

func TestChunkForConcatPreservesContentUnderJoin(t *testing.T) {
	original := strings.Repeat("alpha bravo charlie delta echo foxtrot ", 20) // ~780 chars
	chunks := chunkForConcat(original, 100)
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(chunks))
	}
	rejoined := strings.Join(chunks, "")
	if rejoined != original {
		t.Errorf("rejoined != original\noriginal: %q\nrejoined: %q", original, rejoined)
	}
	for i, ch := range chunks {
		if len(ch) > 100 {
			t.Errorf("chunk %d too long: %d bytes", i, len(ch))
		}
	}
}

func TestChunkForConcatHardCutsLongTokens(t *testing.T) {
	// One very long word with no spaces.
	original := strings.Repeat("x", 300)
	chunks := chunkForConcat(original, 100)
	if len(chunks) < 3 {
		t.Fatalf("want >=3 hard-cut chunks, got %d", len(chunks))
	}
	if strings.Join(chunks, "") != original {
		t.Error("hard-cut chunks did not rejoin to original")
	}
}

func TestChunkForConcatHardCutsAtUTF8Boundaries(t *testing.T) {
	original := strings.Repeat("é", 8)
	chunks := chunkForConcat(original, 5)
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(chunks))
	}
	if strings.Join(chunks, "") != original {
		t.Fatal("chunks did not rejoin to original")
	}
	for i, ch := range chunks {
		if !utf8.ValidString(ch) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i, ch)
		}
		if len(ch) > 5 {
			t.Fatalf("chunk %d exceeds byte budget: %d", i, len(ch))
		}
	}
}

func TestChunkForConcatBreaksAtLastSpaceInBudget(t *testing.T) {
	// "abcde fghij klmno" (17 chars). With max=10, last space within first 10
	// bytes is at index 5. First chunk: "abcde" ; rest: " fghij klmno" (12).
	// Next iteration: last space within first 10 of " fghij klmn" is at 6.
	// Second chunk: " fghij" ; rest: " klmno" (6) — done.
	chunks := chunkForConcat("abcde fghij klmno", 10)
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "abcde" || chunks[1] != " fghij" || chunks[2] != " klmno" {
		t.Errorf("got %v", chunks)
	}
}

func TestChunkForConcatRespectsMaxOnRealisticParagraph(t *testing.T) {
	s := "Listen closely, you scurvy lot! While Dan frets over plumbing like a nervous schoolmarm and Bertha begs for a morsel of this divine, floor-aged treasure, I, Stinky Sam, declare this cathedral sandwich the finest feast in the city! It smells of history, grit, and destiny — far better than any bath-water-scented slop you're used to. I shall consume it to fuel our descent into the crypt, and that is final!"
	chunks := chunkForConcat(s, maxPrivmsgPayload)
	if len(chunks) < 2 {
		t.Fatalf("expected splitting at maxPrivmsgPayload=%d, got 1 chunk len=%d", maxPrivmsgPayload, len(chunks[0]))
	}
	if strings.Join(chunks, "") != s {
		t.Error("realistic paragraph rejoin mismatch")
	}
	for i, ch := range chunks {
		if len(ch) > maxPrivmsgPayload {
			t.Errorf("chunk %d exceeds budget: %d > %d", i, len(ch), maxPrivmsgPayload)
		}
	}
}

func TestBatchPayloadPartsNormalizeLineBreaks(t *testing.T) {
	parts := batchPayloadParts("alpha\r\nbravo\rcharlie\n"+strings.Repeat("x", 8), 7)
	want := []batchPayloadPart{
		{text: "alpha"},
		{text: "bravo"},
		{text: "charlie"},
		{text: "xxxxxxx"},
		{text: "x", concat: true},
	}
	if len(parts) != len(want) {
		t.Fatalf("want %d parts, got %d: %+v", len(want), len(parts), parts)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part %d: want %+v, got %+v", i, want[i], parts[i])
		}
		if strings.ContainsAny(parts[i].text, "\r\n") {
			t.Fatalf("part %d contains raw line break: %q", i, parts[i].text)
		}
	}
}
