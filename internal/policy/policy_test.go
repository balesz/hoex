package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balesz/hoex/internal/config"
)

func load(t *testing.T, yaml string) *Policy {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.ProjectFileName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg.Rules)
}

func TestCheck(t *testing.T) {
	p := load(t, `
allow:
  - prefix: flutter
    description: Flutter
    example: flutter test
  - pattern: 'npm (test|run build)'
    description: npm
    example: npm test
`)
	tests := []struct {
		argv   []string
		allow  bool
		reason string
	}{
		{[]string{"flutter", "test"}, true, ""},
		{[]string{"flutter", "test", "--name", "a b"}, true, ""},
		{[]string{"npm", "run", "build"}, true, ""},
		{[]string{"flutterx"}, false, "command not allowed: flutterx"},
		{[]string{"npm", "install"}, false, "command not allowed"},
		{[]string{"rm", "-rf", "/"}, false, "command not allowed"},
		{nil, false, "empty command"},
		{[]string{""}, false, "empty command"},
		{[]string{"flutter", "test;", "rm"}, false, `chaining is not allowed (argument contains ";")`},
		{[]string{"flutter", "test", "&&", "rm"}, false, `"&&"`},
		{[]string{"flutter", "a||b"}, false, `"||"`},
		{[]string{"flutter", "test", "|", "grep"}, false, `"|"`},
		{[]string{"flutter", "a\nrm"}, false, `"\n"`},
		{[]string{"flutter", "$(rm)"}, false, `"$("`},
		{[]string{"flutter", "`rm`"}, false, "\"`\""},
		// A single & or $ is not chaining.
		{[]string{"flutter", "a&b", "$HOME"}, true, ""},
	}
	for _, tt := range tests {
		d := p.Check(tt.argv)
		if d.Allowed != tt.allow || !strings.Contains(d.Reason, tt.reason) {
			t.Errorf("Check(%q) = %+v, want allowed=%v reason~%q", tt.argv, d, tt.allow, tt.reason)
		}
		if d.Allowed && d.Rule == nil {
			t.Errorf("Check(%q): allowed without rule", tt.argv)
		}
	}
}

func TestCheckChainingAllowedWhenDisabled(t *testing.T) {
	p := load(t, `
deny_chaining: false
allow:
  - prefix: flutter
    description: Flutter
    example: flutter test
`)
	if d := p.Check([]string{"flutter", "test", ";"}); !d.Allowed {
		t.Errorf("rejected with deny_chaining: false: %s", d.Reason)
	}
}

func TestCheckStricterLevelWins(t *testing.T) {
	root := t.TempDir()
	userFile := filepath.Join(t.TempDir(), "config.yaml")
	user := "projects:\n  - path: " + root + "\n    deny_chaining: false\n    allow:\n      - prefix: flutter\n        description: d\n        example: flutter test\n"
	project := "deny_chaining: true\nallow:\n  - prefix: flutter test\n    description: d\n    example: flutter test\n"
	if err := os.WriteFile(userFile, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectFileName), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root, userFile)
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg.Rules)

	// Both levels match: the project level denies chaining.
	if d := p.Check([]string{"flutter", "test", ";"}); d.Allowed {
		t.Error("allowed; the stricter matching level should win")
	}
	// Only the user level matches: chaining is allowed there.
	if d := p.Check([]string{"flutter", "build", ";"}); !d.Allowed {
		t.Errorf("rejected: %s", d.Reason)
	}
}
