package skill

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balesz/hoex/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const projectConfig = `allow:
  - prefix: fvm  flutter
    description: Flutter build, test and run through FVM.
    example: fvm flutter test
  - prefix: fvm dart
    description: Dart tools through FVM.
    example: fvm dart format .
  - pattern: 'npm (test|run build)'
    description: Run the tests and the build.
    example: npm test
`

func load(t *testing.T, root, project string) *config.Config {
	t.Helper()
	cfg, err := config.LoadData(root, "", []byte(project))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the generated content (go test ./internal/skill -update to accept):\n%s", file, got)
	}
}

func TestGenerateClaude(t *testing.T) {
	got, err := Generate("claude", load(t, t.TempDir(), projectConfig))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "claude.md", got)
}

func TestGenerateClaudeChainingAllowed(t *testing.T) {
	got, err := Generate("claude", load(t, t.TempDir(), "deny_chaining: false\n"+projectConfig))
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "are rejected:\nrun separate hoex commands") {
		t.Error("chaining note present although no rule denies chaining")
	}
	if strings.Count(s, "Chaining characters are allowed") != 3 {
		t.Error("rules do not say that chaining is allowed")
	}
}

func TestGenerateClaudeNoRules(t *testing.T) {
	got, err := Generate("claude", load(t, t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "No commands are allowed yet.") {
		t.Errorf("no-rules description missing:\n%s", got)
	}
}

func TestInstallAndUpdate(t *testing.T) {
	root := t.TempDir()
	cfg := load(t, root, projectConfig)

	if written, err := Update(root, cfg); err != nil || len(written) != 0 {
		t.Fatalf("Update without an installed skill wrote %v, %v", written, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Fatal("Update created .claude")
	}

	written, err := Install("claude", root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(written, " ") != ".claude/skills/hoex/SKILL.md .claude/settings.json" {
		t.Errorf("written %v", written)
	}
	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "settings-new.json", settings)

	// Installing again changes nothing in the settings.
	if written, err = Install("claude", root, cfg); err != nil || len(written) != 1 {
		t.Errorf("second install wrote %v, %v", written, err)
	}

	cfg = load(t, root, projectConfig+"  - prefix: make\n    description: Make.\n    example: make all\n")
	if written, err = Update(root, cfg); err != nil || len(written) != 1 {
		t.Fatalf("Update wrote %v, %v", written, err)
	}
	skill, err := os.ReadFile(filepath.Join(root, ".claude", "skills", "hoex", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "### `make`") {
		t.Error("updated skill does not list the new rule")
	}
}

func TestInstallUnknownAgent(t *testing.T) {
	_, err := Install("codex", t.TempDir(), load(t, t.TempDir(), ""))
	if err == nil || !strings.Contains(err.Error(), "supported: claude") {
		t.Errorf("err = %v", err)
	}
}

func TestMergeDeny(t *testing.T) {
	existing := `{
  "permissions": {
    "allow": ["Bash(hoex --mode sandbox *)"],
    "deny": ["Read(./.env)", "Edit(/.hoex.yaml)"]
  },
  "model": "opus"
}
`
	out, changed, err := mergeDeny([]byte(existing), claudeDeny)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	golden(t, "settings-merged.json", out)

	if _, changed, _ := mergeDeny(out, claudeDeny); changed {
		t.Error("merging twice changed the file again")
	}
	for _, bad := range []string{"[]", `{"permissions": []}`, `{"permissions": {"deny": "x"}}`, "{"} {
		if _, _, err := mergeDeny([]byte(bad), claudeDeny); err == nil {
			t.Errorf("no error for %s", bad)
		}
	}
}

func TestSymlinkedDirectoryNotFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".claude", "skills", "hoex")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("claude", root, load(t, root, projectConfig)); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("err = %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote outside the project: %v", entries)
	}
}
