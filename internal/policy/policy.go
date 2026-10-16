// Package policy decides whether a command from the sandbox may run.
package policy

import (
	"fmt"
	"strings"

	"github.com/balesz/hoex/internal/config"
)

// chainTokens are the command chaining marks rejected by deny_chaining.
// Without a shell they would not run anyway; rejecting them gives the agent
// a clear error instead of a confusing one from the command.
var chainTokens = []string{";", "&&", "||", "|", "\n", "\r", "$(", "`"}

// Policy holds the allow rules of a configuration.
type Policy struct {
	rules []*config.Rule
}

// New returns a policy for the given rules.
func New(rules []*config.Rule) *Policy {
	return &Policy{rules: rules}
}

// Decision is the result of Check.
type Decision struct {
	Allowed bool
	// Rule is the first matching rule, if any.
	Rule *config.Rule
	// Reason explains a rejection.
	Reason string
}

// Check decides whether argv may run. A command is allowed when at least one
// rule matches it, and it contains no chaining mark if any matching rule comes
// from a level with deny_chaining (the stricter level wins).
func (p *Policy) Check(argv []string) Decision {
	if len(argv) == 0 || argv[0] == "" {
		return Decision{Reason: "empty command"}
	}
	var matched []*config.Rule
	for _, r := range p.rules {
		if r.Matches(argv) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return Decision{Reason: fmt.Sprintf("command not allowed: %s", strings.Join(argv, " "))}
	}
	if tok := chainToken(argv); tok != "" {
		for _, r := range matched {
			if r.DenyChaining {
				return Decision{Rule: r, Reason: fmt.Sprintf(
					"command chaining is not allowed (argument contains %q); commands run without a shell, pipe the output in the sandbox instead",
					tok)}
			}
		}
	}
	return Decision{Allowed: true, Rule: matched[0]}
}

func chainToken(argv []string) string {
	for _, a := range argv {
		for _, t := range chainTokens {
			if strings.Contains(a, t) {
				return t
			}
		}
	}
	return ""
}
