// Package approval remembers the last approved content of each project's
// config file on the host, outside the project, where the sandbox cannot
// change it. The host compares it with the file at startup, so a change made
// while the host was not running also needs approval.
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Store keeps one file per project in Dir.
type Store struct {
	Dir string
}

// DefaultDir returns $XDG_STATE_HOME/hoex/approved, or
// ~/.local/state/hoex/approved on every platform.
func DefaultDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "hoex", "approved"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "hoex", "approved"), nil
}

// path names the file of a project by a hash of its root.
func (s Store) path(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:16])+".yaml")
}

// Load returns the approved content for the project at root; ok is false if
// nothing was approved yet.
func (s Store) Load(root string) (data []byte, ok bool, err error) {
	data, err = os.ReadFile(s.path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Save records data as the approved content for the project at root.
func (s Store) Save(root string, data []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	file := s.path(root)
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
