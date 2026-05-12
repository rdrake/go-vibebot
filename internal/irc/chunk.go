package irc

import (
	"strings"
	"unicode/utf8"
)

// maxPrivmsgPayload is the conservative byte budget for one PRIVMSG payload
// when wrapped in a draft/multiline BATCH. The 512-byte IRC frame must
// also carry the message tags (~`@batch=NN;draft/multiline-concat ` ≈ 45
// bytes), the prefix injected by the server (~120 bytes for hostmask), the
// command, the target, and CRLF. 350 leaves headroom for all of those on
// reasonable channels and hostnames.
const maxPrivmsgPayload = 350

// chunkForConcat splits s into chunks each <= max bytes, suitable for
// emission as a draft/multiline BATCH with draft/multiline-concat tags on
// continuation lines. Concatenating the returned chunks (with empty
// separator) reproduces the original text exactly: continuation chunks
// retain their leading whitespace so the receiver reassembles word
// boundaries correctly.
//
// Behavior:
//   - Empty / whitespace-only input returns nil.
//   - Input <= max returns a single chunk.
//   - Splits happen at the last whitespace within the budget; if a token is
//     longer than max it is hard-cut (rare; URLs, base64 blobs).
//
// The function is pure and safe for concurrent use.
func chunkForConcat(s string, max int) []string {
	if max <= 0 {
		return nil
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if len(s) <= max {
		return []string{s}
	}
	var chunks []string
	rest := s
	for len(rest) > max {
		searchLen := max
		if searchLen > len(rest) {
			searchLen = len(rest)
		}
		cut := strings.LastIndex(rest[:searchLen], " ")
		if cut <= 0 {
			// No whitespace in budget — hard cut at max, backing up to a
			// UTF-8 boundary so IRC payload chunks remain valid text.
			cut = utf8Boundary(rest, searchLen)
		}
		chunks = append(chunks, rest[:cut])
		rest = rest[cut:]
	}
	if rest != "" {
		chunks = append(chunks, rest)
	}
	return chunks
}

func utf8Boundary(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	if max > 0 {
		return max
	}
	_, size := utf8.DecodeRuneInString(s)
	return size
}

type batchPayloadPart struct {
	text   string
	concat bool
}

func batchPayloadParts(s string, max int) []batchPayloadPart {
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	var parts []batchPayloadPart
	for _, line := range strings.Split(normalized, "\n") {
		chunks := chunkForConcat(line, max)
		for i, ch := range chunks {
			parts = append(parts, batchPayloadPart{
				text:   ch,
				concat: i > 0,
			})
		}
	}
	return parts
}
