package runner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func init() { KillDelay = 300 * time.Millisecond }

type collected struct {
	stdout, stderr strings.Builder
}

func (c *collected) out(ch Chunk) error {
	if ch.Stderr {
		c.stderr.Write(ch.Data)
	} else {
		c.stdout.Write(ch.Data)
	}
	return nil
}

func TestRunExitCodeAndOutput(t *testing.T) {
	var c collected
	code, err := Run(context.Background(), []string{"sh", "-c", "echo out; echo err >&2; exit 3"}, t.TempDir(), c.out)
	if err != nil || code != 3 || c.stdout.String() != "out\n" || c.stderr.String() != "err\n" {
		t.Errorf("code=%d err=%v stdout=%q stderr=%q", code, err, c.stdout.String(), c.stderr.String())
	}
}

func TestRunNotFound(t *testing.T) {
	var c collected
	code, err := Run(context.Background(), []string{"hoex-no-such-command"}, t.TempDir(), c.out)
	if err != nil || code != ExitNotFound || !strings.Contains(c.stderr.String(), "hoex-no-such-command") {
		t.Errorf("code=%d err=%v stderr=%q", code, err, c.stderr.String())
	}
}

func TestRunNotExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var c collected
	code, _ := Run(context.Background(), []string{"./x.sh"}, dir, c.out)
	if code != ExitCannotExecute {
		t.Errorf("code=%d stderr=%q", code, c.stderr.String())
	}
}

// startBackground runs script under Run in the background. The script must
// write the PID of a long-running child to the file "pid".
func startBackground(t *testing.T, ctx context.Context, script string) (pid int, result chan error) {
	t.Helper()
	dir := t.TempDir()
	result = make(chan error, 1)
	go func() {
		var c collected
		_, err := Run(ctx, []string{"sh", "-c", script}, dir, c.out)
		result <- err
	}()
	pidFile := filepath.Join(dir, "pid")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid, result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("script did not write its pid")
	return 0, nil
}

// alive reports whether pid runs and is not a zombie.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true // no procfs (macOS): signal 0 succeeded
	}
	fields := strings.Fields(string(data))
	return len(fields) < 3 || fields[2] != "Z"
}

func waitDead(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after %v", pid, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pid, result := startBackground(t, ctx, "sleep 1000 & echo $! > pid; wait")

	cancel()
	// SIGTERM to the group must be enough: no waiting for the SIGKILL delay.
	select {
	case err := <-result:
		if err == nil {
			t.Error("Run returned no error after cancel")
		}
	case <-time.After(KillDelay / 2):
		t.Fatal("Run did not return before the SIGKILL delay; was the group signaled?")
	}
	waitDead(t, pid, KillDelay/2)
}

func TestCancelEscalatesToSIGKILL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// The ignored SIGTERM disposition is inherited by the sleep child.
	pid, result := startBackground(t, ctx, "trap '' TERM; sleep 1000 & echo $! > pid; wait")

	start := time.Now()
	cancel()
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if d := time.Since(start); d < KillDelay {
		t.Errorf("returned after %v, before the SIGKILL delay", d)
	}
	waitDead(t, pid, time.Second)
}

func TestBackgroundChildDoesNotBlockForever(t *testing.T) {
	var c collected
	dir := t.TempDir()
	start := time.Now()
	code, err := Run(context.Background(), []string{"sh", "-c", "sleep 1000 & echo $! > pid; echo done"}, dir, c.out)
	if data, _ := os.ReadFile(filepath.Join(dir, "pid")); len(data) > 0 {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if err != nil || code != 0 || c.stdout.String() != "done\n" {
		t.Errorf("code=%d err=%v stdout=%q", code, err, c.stdout.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}
