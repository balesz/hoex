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

	"github.com/balesz/hoex/internal/projfs"
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
// directory out of version control. Like every write of the host into the
// project, it does not follow links to outside the project.
func EnsureDir(root string) error {
	return projfs.CreateFile(root, filepath.Join(Dir, ".gitignore"), []byte("*\n"), 0o644)
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
	return projfs.WriteFile(root, filepath.Join(Dir, FileName), append(data, '\n'), 0o600)
}

// Remove deletes the connection file of root. A missing file is not an error.
func Remove(root string) error {
	return projfs.Remove(root, filepath.Join(Dir, FileName))
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
