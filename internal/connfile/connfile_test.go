package connfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFindRemove(t *testing.T) {
	root := t.TempDir()
	want := Info{Port: 47800, Token: "secret", PID: 42}
	if err := Write(root, want); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if data, _ := os.ReadFile(filepath.Join(root, Dir, ".gitignore")); string(data) != "*\n" {
		t.Errorf(".gitignore = %q", data)
	}

	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	gotRoot, got, err := Find(deep)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || got != want {
		t.Errorf("Find = %s %+v, want %s %+v", gotRoot, got, root, want)
	}

	if err := Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if _, _, err := Find(deep); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find after Remove: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, Dir)); len(entries) != 1 {
		t.Errorf("leftover files in %s: %v", Dir, entries)
	}
}

func TestFindInvalidFile(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte(`{"port": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Find(root); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Find = %v, want a parse error", err)
	}
}
