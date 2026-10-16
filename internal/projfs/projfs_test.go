package projfs

import (
	"os"
	"path/filepath"
	"testing"
)

// escape sets up a project whose dir "link" points outside, next to a
// victim file that must never change.
func escape(t *testing.T) (root, victim string) {
	t.Helper()
	root = t.TempDir()
	outside := t.TempDir()
	victim = filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "filelink")); err != nil {
		t.Fatal(err)
	}
	return root, victim
}

func TestSymlinksOutsideRefused(t *testing.T) {
	root, victim := escape(t)
	if err := WriteFile(root, "dirlink/victim", []byte("x"), 0o644); err == nil {
		t.Error("WriteFile through a directory link succeeded")
	}
	if err := WriteFile(root, "../victim", []byte("x"), 0o644); err == nil {
		t.Error("WriteFile with .. succeeded")
	}
	if f, err := OpenAppend(root, "filelink", 0o644); err == nil {
		_ = f.Close()
		t.Error("OpenAppend through a file link succeeded")
	}
	if f, err := OpenAppend(root, "dirlink/victim", 0o644); err == nil {
		_ = f.Close()
		t.Error("OpenAppend through a directory link succeeded")
	}
	if err := CreateFile(root, "dirlink/new", []byte("x"), 0o644); err == nil {
		t.Error("CreateFile through a directory link succeeded")
	}
	if _, err := ReadFile(root, "filelink"); err == nil {
		t.Error("ReadFile through a link succeeded")
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Errorf("victim changed: %q", data)
	}
	if entries, _ := os.ReadDir(filepath.Dir(victim)); len(entries) != 1 {
		t.Errorf("files created outside: %v", entries)
	}
}

func TestWriteFileReplacesLinkItself(t *testing.T) {
	root, victim := escape(t)
	if err := WriteFile(root, "filelink", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Errorf("victim changed: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "filelink")); string(data) != "new" {
		t.Errorf("link not replaced: %q", data)
	}
}

func TestFiles(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "a/b/c.txt", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "a/b/c.txt", []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(root, "a/b/c.txt"); err != nil || string(data) != "2" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if st, _ := os.Stat(filepath.Join(root, "a/b/c.txt")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "a/b")); len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
	if err := CreateFile(root, "a/b/c.txt", []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := ReadFile(root, "a/b/c.txt"); string(data) != "2" {
		t.Error("CreateFile overwrote an existing file")
	}
	if data, err := ReadFile(root, "missing"); data != nil || err != nil {
		t.Errorf("missing file: %q, %v", data, err)
	}
	if err := Remove(root, "a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, "a/b/c.txt"); err != nil {
		t.Errorf("removing a missing file: %v", err)
	}
}
