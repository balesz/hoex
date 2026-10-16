// Package connfile reads and writes .hoex/connection, which tells the sandbox
// side how to reach the host daemon of a project.
package connfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// Dir is the hoex state directory in the project root.
	Dir = ".hoex"
	// FileName is the connection file inside Dir.
	FileName = "connection"
)

// ErrNotFound is returned by Find when no connection file exists.
var ErrNotFound = errors.New("connection file not found")

// Info is the content of the connection file.
type Info struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// Path returns the connection file path for a project root.
func Path(root string) string {
	return filepath.Join(root, Dir, FileName)
}

// EnsureDir creates root/.hoex with a .gitignore that keeps the whole
// directory out of version control.
func EnsureDir(root string) error {
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(ignore, []byte("*\n"), 0o644)
	}
	return nil
}

// Write atomically writes the connection file of root with mode 0600.
func Write(root string, info Info) error {
	if err := EnsureDir(root); err != nil {
		return err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(root, Dir), FileName+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path(root))
}

// Remove deletes the connection file of root. A missing file is not an error.
func Remove(root string) error {
	err := os.Remove(Path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Find looks for .hoex/connection in dir and its parents, like git looks for
// .git. It returns the project root and the parsed file.
func Find(dir string) (root string, info Info, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", Info{}, err
	}
	for {
		data, err := os.ReadFile(Path(dir))
		if err == nil {
			if err := json.Unmarshal(data, &info); err != nil {
				return "", Info{}, fmt.Errorf("%s: %w", Path(dir), err)
			}
			if info.Port == 0 || info.Token == "" {
				return "", Info{}, fmt.Errorf("%s: missing port or token", Path(dir))
			}
			return dir, info, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", Info{}, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", Info{}, ErrNotFound
		}
		dir = parent
	}
}
