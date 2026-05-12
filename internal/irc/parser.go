package irc

import "strings"

// Command is the parsed result of an IRC line that starts with "!".
// Verb is lowercased; Args is everything after the verb, trimmed.
type Command struct {
	Verb string
	Args string
}

// ParseCommand returns the parsed command and ok=true if text begins with
// "!" and has a non-empty verb. Otherwise ok=false (caller should ignore).
//
// Splitting on first whitespace only — Args remains free-form for verbs
// whose argument is a sentence (e.g. !inject).
func ParseCommand(text string) (Command, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "!") {
		return Command{}, false
	}
	parts := strings.SplitN(text, " ", 2)
	verb := strings.ToLower(strings.TrimPrefix(parts[0], "!"))
	if verb == "" {
		return Command{}, false
	}
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return Command{Verb: verb, Args: args}, true
}
