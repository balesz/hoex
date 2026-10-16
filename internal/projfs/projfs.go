// Package projfs writes files inside the project for the host. The sandbox
// can change the project at any time, for example replace .hoex or .claude
// with a symbolic link to a file of the user's (~/.bashrc). Every operation
// here goes through os.Root, which refuses paths that lead outside the
// project, also through symbolic links, at the moment of the operation.
package projfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile writes name (relative to root) atomically: a temporary file in
// the same directory, then a rename. Missing directories are created.
func WriteFile(root, name string, data []byte, perm os.FileMode) (err error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := r.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp-"+hex.EncodeToString(suffix))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = r.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return r.Rename(tmp, name)
}

// CreateFile creates name with data unless it exists; an existing file,
// also a symbolic link, is left alone.
func CreateFile(root, name string, data []byte, perm os.FileMode) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := r.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// OpenAppend opens name for appending, creating it if needed.
func OpenAppend(root, name string, perm os.FileMode) (*os.File, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := r.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, err
	}
	return r.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, perm)
}

// ReadFile reads name; a missing file is nil without an error.
func ReadFile(root, name string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := r.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// Remove deletes name; a missing file is not an error.
func Remove(root, name string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
