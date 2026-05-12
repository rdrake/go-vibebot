package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validate cross-checks character/group references and returns a joined
// error listing every problem found. Run after Load*; flagging all issues
// at parse-time beats a chain of one-at-a-time wiring failures.
func Validate(chars []CharacterSpec, groups []GroupSpec) error {
	known := make(map[string]struct{}, len(chars))
	var problems []string

	for _, c := range chars {
		if c.ID == "" {
			problems = append(problems, "character with empty id")
			continue
		}
		if _, dup := known[c.ID]; dup {
			problems = append(problems, fmt.Sprintf("duplicate character id %q", c.ID))
			continue
		}
		known[c.ID] = struct{}{}
	}

	for _, g := range groups {
		if g.ID == "" {
			problems = append(problems, "group with empty id")
		}
		if g.Leader == "" {
			problems = append(problems, fmt.Sprintf("group %q has no leader", g.ID))
		} else if _, ok := known[g.Leader]; !ok {
			problems = append(problems, fmt.Sprintf("group %q leader %q not in characters", g.ID, g.Leader))
		}
		leaderInMembers := false
		members := make(map[string]struct{}, len(g.Members))
		for _, m := range g.Members {
			if _, dup := members[m]; dup {
				problems = append(problems, fmt.Sprintf("group %q duplicate member %q", g.ID, m))
				continue
			}
			members[m] = struct{}{}
			if _, ok := known[m]; !ok {
				problems = append(problems, fmt.Sprintf("group %q member %q not in characters", g.ID, m))
			}
			if m == g.Leader {
				leaderInMembers = true
			}
		}
		if g.Leader != "" && !leaderInMembers {
			problems = append(problems, fmt.Sprintf("group %q leader %q not in members", g.ID, g.Leader))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("config: " + strings.Join(problems, "; "))
}
