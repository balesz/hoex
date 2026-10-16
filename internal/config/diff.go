package config

import "fmt"

// Diff describes how the configuration changes from old to new, one line per
// difference: "+ rule" for a new rule, "- rule" for a removed one, "~ rule"
// for a rule whose description or example changed, and "~ port" for a new
// port. It is empty if nothing that matters changed.
func Diff(old, new *Config) []string {
	var lines []string
	if old.Port != new.Port {
		lines = append(lines, fmt.Sprintf("~ port %d → %d (takes effect after a restart)", old.Port, new.Port))
	}

	// Rules are matched by what they allow; the same rule may appear more
	// than once.
	remaining := make(map[string][]*Rule)
	for _, r := range old.Rules {
		remaining[ruleKey(r)] = append(remaining[ruleKey(r)], r)
	}
	var added []string
	for _, r := range new.Rules {
		k := ruleKey(r)
		if olds := remaining[k]; len(olds) > 0 {
			o := olds[0]
			remaining[k] = olds[1:]
			if o.Description != r.Description || o.Example != r.Example {
				lines = append(lines, "~ "+k+" (description or example)")
			}
			continue
		}
		added = append(added, "+ "+k)
	}
	for _, r := range old.Rules {
		k := ruleKey(r)
		if olds := remaining[k]; len(olds) > 0 && olds[0] == r {
			remaining[k] = olds[1:]
			lines = append(lines, "- "+k)
		}
	}
	return append(lines, added...)
}

// ruleKey is what a rule allows, as shown in a diff.
func ruleKey(r *Rule) string {
	if !r.DenyChaining {
		return r.String() + " (chaining allowed)"
	}
	return r.String()
}
