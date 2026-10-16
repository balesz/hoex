package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two examples from docs/PRD.md.
const (
	prdUserConfig = `projects:
  - path: %s
    deny_chaining: true
    allow:
      - prefix: flutter
        description: Flutter build, teszt, futtatás
        example: flutter test
      - prefix: dart
        description: Dart eszközök
        example: dart format .
`
	prdProjectConfig = `deny_chaining: true
allow:
  - pattern: '^npm (test|run build)$'
    description: Tesztek és build futtatása
    example: npm test
  - pattern: '^\./scripts/release\.sh$'
    description: Release script
    example: ./scripts/release.sh
`
)

// setup creates a project root and a user config path inside a temp dir.
func setup(t *testing.T, user, project string) (root, userFile string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "project")
	userFile = filepath.Join(dir, "config.yaml")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if user != "" {
		write(t, userFile, user)
	}
	if project != "" {
		write(t, filepath.Join(root, ProjectFileName), project)
	}
	return root, userFile
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, root, userFile string) *Config {
	t.Helper()
	cfg, err := Load(root, userFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadPRDExamples(t *testing.T) {
	root, userFile := setup(t, "", prdProjectConfig)
	write(t, userFile, fmt.Sprintf(prdUserConfig, root))

	cfg := mustLoad(t, root, userFile)

	var got []string
	for _, r := range cfg.Rules {
		got = append(got, r.String())
		if !r.DenyChaining {
			t.Errorf("%s: DenyChaining = false", r)
		}
	}
	want := []string{
		"prefix flutter",
		"prefix dart",
		`pattern ^npm (test|run build)$`,
		`pattern ^\./scripts/release\.sh$`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rules =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("warnings: %v", cfg.Warnings)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.ProjectFile != filepath.Join(root, ProjectFileName) {
		t.Errorf("ProjectFile = %s", cfg.ProjectFile)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	root, userFile := setup(t, "", "")
	cfg := mustLoad(t, root, userFile)
	if len(cfg.Rules) != 0 || cfg.Port != DefaultPort {
		t.Errorf("got %+v", cfg)
	}
}

func TestUserLevelOnlyAppliesToItsProject(t *testing.T) {
	root, userFile := setup(t, "", "")
	write(t, userFile, fmt.Sprintf(prdUserConfig, "/somewhere/else"))
	if cfg := mustLoad(t, root, userFile); len(cfg.Rules) != 0 {
		t.Errorf("rules from another project: %v", cfg.Rules)
	}
}

func TestUserLevelPathThroughSymlink(t *testing.T) {
	root, userFile := setup(t, "", "")
	link := filepath.Join(filepath.Dir(root), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	write(t, userFile, fmt.Sprintf(prdUserConfig, link))
	if cfg := mustLoad(t, root, userFile); len(cfg.Rules) != 2 {
		t.Errorf("got %d rules, want 2", len(cfg.Rules))
	}
}

func TestPort(t *testing.T) {
	user := func(root string) string {
		return fmt.Sprintf("projects:\n  - path: %s\n    port: 47900\n", root)
	}
	t.Run("user level", func(t *testing.T) {
		root, userFile := setup(t, "", "")
		write(t, userFile, user(root))
		if got := mustLoad(t, root, userFile).Port; got != 47900 {
			t.Errorf("port = %d", got)
		}
	})
	t.Run("project level wins", func(t *testing.T) {
		root, userFile := setup(t, "", "port: 48000\n")
		write(t, userFile, user(root))
		if got := mustLoad(t, root, userFile).Port; got != 48000 {
			t.Errorf("port = %d", got)
		}
	})
}

func TestDenyChainingDefaultsToTrue(t *testing.T) {
	root, _ := setup(t, "", "allow:\n  - prefix: go\n    description: d\n    example: go test\n")
	if r := mustLoad(t, root, "").Rules[0]; !r.DenyChaining {
		t.Error("DenyChaining = false, want true by default")
	}
	root, _ = setup(t, "", "deny_chaining: false\nallow:\n  - prefix: go\n    description: d\n    example: go test\n")
	if r := mustLoad(t, root, "").Rules[0]; r.DenyChaining {
		t.Error("DenyChaining = true, want false")
	}
}

func TestProjectErrors(t *testing.T) {
	tests := []struct {
		name    string
		project string
		want    string // ":line: message" substring
	}{
		{"yaml syntax", "allow:\n  - prefix: [\n", "line 2"},
		{"unknown top-level key", "allow: []\nallows: []\n", ":2: unknown key \"allows\""},
		{"unknown rule key", "allow:\n  - prefix: go\n    descr: x\n    description: d\n    example: go\n", ":3: unknown key \"descr\""},
		{"both prefix and pattern", "allow:\n  - prefix: go\n    pattern: go\n    description: d\n    example: go\n", ":2: rule must have either prefix or pattern, not both"},
		{"neither prefix nor pattern", "allow:\n  - description: d\n    example: go\n", ":2: rule must have a prefix or a pattern"},
		{"missing description", "allow:\n  - prefix: go\n    example: go\n", ":2: rule is missing description"},
		{"missing example", "allow:\n  - prefix: go\n    description: d\n", ":2: rule is missing example"},
		{"empty example", "allow:\n  - prefix: go\n    description: d\n    example: ''\n", ":4: example is empty"},
		{"empty prefix", "allow:\n  - prefix: '  '\n    description: d\n    example: go\n", ":2: prefix is empty"},
		{"bad regex", "allow:\n  - prefix: go\n    description: d\n    example: go\n  - pattern: 'npm (test'\n    description: d\n    example: npm test\n", ":5: error parsing regexp"},
		{"allow not a list", "allow: go\n", ":1: allow must be a list"},
		{"rule not a mapping", "allow:\n  - go\n", ":2: rule must be a mapping"},
		{"deny_chaining not bool", "deny_chaining: yes please\n", ":1: deny_chaining must be true or false"},
		{"port out of range", "port: 70000\n", ":1: port must be an integer"},
		{"port not a number", "port: abc\n", ":1: port must be an integer"},
		{"top level not a mapping", "- go\n", ":1: project config must be a mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := setup(t, "", tt.project)
			_, err := Load(root, "")
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), ProjectFileName) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want file name and %q", err, tt.want)
			}
		})
	}
}

func TestUserErrors(t *testing.T) {
	tests := []struct {
		name string
		user string
		want string
	}{
		{"relative path", "projects:\n  - path: dev/app\n", ":2: path must be absolute"},
		{"missing path", "projects:\n  - allow: []\n", ":2: project entry is missing path"},
		{"unknown key", "project: []\n", ":1: unknown key \"project\""},
		{"projects not a list", "projects: x\n", ":1: projects must be a list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, userFile := setup(t, tt.user, "")
			_, err := Load(root, userFile)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAllErrorsReported(t *testing.T) {
	root, _ := setup(t, "", "allow:\n  - prefix: go\n  - pattern: '('\n    description: d\n    example: x\n")
	_, err := Load(root, "")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{":2: rule is missing description", ":2: rule is missing example", ":3: error parsing regexp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestWarnings(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want string
	}{
		{"bash prefix", "prefix: bash\n    description: d\n    example: bash -c ls", `allows "bash -c"`},
		{"env prefix", "prefix: env\n    description: d\n    example: env FOO=1 make", `allows "env -c"`},
		{"shell via pattern", "pattern: 'sh .*'\n    description: d\n    example: sh -c ls", `allows "sh -c"`},
		{"example not allowed", "prefix: flutter\n    description: d\n    example: dart test", `example "dart test" is not allowed`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := setup(t, "", "allow:\n  - "+tt.rule+"\n")
			cfg := mustLoad(t, root, "")
			if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], tt.want) {
				t.Errorf("warnings = %q, want one containing %q", cfg.Warnings, tt.want)
			}
		})
	}
}

func TestRuleMatches(t *testing.T) {
	prefix := func(p string) *Rule { r := &Rule{Prefix: p}; mustRule(t, r); return r }
	pattern := func(p string) *Rule { r := &Rule{Pattern: p}; mustRule(t, r); return r }
	tests := []struct {
		rule *Rule
		argv []string
		want bool
	}{
		{prefix("flutter"), []string{"flutter"}, true},
		{prefix("flutter"), []string{"flutter", "test"}, true},
		{prefix("flutter"), []string{"flutterx"}, false},
		{prefix("flutter"), []string{"flutterx", "test"}, false},
		{prefix("flutter"), []string{"/usr/bin/flutter"}, false},
		{prefix("flutter pub"), []string{"flutter", "pub", "get"}, true},
		{prefix("flutter pub"), []string{"flutter", "test"}, false},
		{prefix("flutter  pub"), []string{"flutter", "pub"}, true},
		{prefix("flutter pub"), []string{"flutter"}, false},
		{pattern("^npm (test|run build)$"), []string{"npm", "test"}, true},
		{pattern("^npm (test|run build)$"), []string{"npm", "run", "build"}, true},
		{pattern("^npm (test|run build)$"), []string{"npm", "test", "--x"}, false},
		// Without anchors the pattern still matches the whole command.
		{pattern("npm test"), []string{"npm", "test"}, true},
		{pattern("npm test"), []string{"npm", "test", "x"}, false},
		{pattern("npm test"), []string{"x", "npm", "test"}, false},
		// Alternation must not escape the implicit anchors.
		{pattern("npm test|ls"), []string{"npm", "test", "&&", "rm"}, false},
		{pattern("npm test|ls"), []string{"ls"}, true},
	}
	for _, tt := range tests {
		if got := tt.rule.Matches(tt.argv); got != tt.want {
			t.Errorf("%s matches %q = %v, want %v", tt.rule, tt.argv, got, tt.want)
		}
	}
}

func mustRule(t *testing.T, r *Rule) {
	t.Helper()
	if err := newRule(r); err != nil {
		t.Fatal(err)
	}
}

func TestUserFilePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p, _ := UserFilePath(); p != "/xdg/hoex/config.yaml" {
		t.Errorf("with XDG_CONFIG_HOME: %s", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if p, _ := UserFilePath(); p != "/home/u/.config/hoex/config.yaml" {
		t.Errorf("without XDG_CONFIG_HOME: %s", p)
	}
}
