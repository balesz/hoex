package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// watchFile runs Watch on a temp file and returns a channel that gets a value
// for every check.
func watchFile(t *testing.T, interval time.Duration) (string, <-chan struct{}) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.yaml")
	checks := make(chan struct{}, 100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, file, Options{Interval: interval, Settle: 10 * time.Millisecond, OnError: func(err error) { t.Error(err) }},
			func() { checks <- struct{}{} })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(50 * time.Millisecond) // let the watcher start
	return file, checks
}

func waitCheck(t *testing.T, checks <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-checks:
	case <-time.After(3 * time.Second):
		t.Fatalf("no check after %s", what)
	}
}

func TestFileEvents(t *testing.T) {
	file, checks := watchFile(t, time.Hour)
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCheck(t, checks, "create")

	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
	waitCheck(t, checks, "atomic replace")

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	waitCheck(t, checks, "remove")
}

func TestOtherFilesIgnored(t *testing.T) {
	file, checks := watchFile(t, time.Hour)
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "other"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-checks:
		t.Error("checked after a change to another file")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestInterval(t *testing.T) {
	_, checks := watchFile(t, 20*time.Millisecond)
	waitCheck(t, checks, "an interval")
}
