// Package runner starts a command on the host without a shell and streams
// its output.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Exit codes for commands that could not be started, as a shell reports them.
const (
	ExitCannotExecute = 126
	ExitNotFound      = 127
)

// KillDelay is how long a canceled command's process group gets between
// SIGTERM and SIGKILL. It also bounds how long Run waits for output pipes
// that a background child keeps open after the command exited.
var KillDelay = 5 * time.Second

// Chunk is a piece of command output.
type Chunk struct {
	Stderr bool
	Data   []byte
}

// OutputFunc receives output in the order it was read. It is called from a
// single goroutine. If it returns an error the command is stopped.
type OutputFunc func(Chunk) error

// Run starts argv in dir with the environment of the current process, sends
// its output to out and returns the exit code. A command killed by a signal
// returns 128+signal. A command that cannot be started returns 126 or 127 and
// a message on stderr, like a shell. The error is non-nil only if out failed
// or ctx was canceled.
//
// The command runs in its own process group. When ctx is canceled the whole
// group gets SIGTERM, then SIGKILL after KillDelay.
func Run(ctx context.Context, argv []string, dir string, out OutputFunc) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunks := make(chan Chunk)
	exited := make(chan struct{})

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = chunkWriter{stderr: false, ch: chunks}
	cmd.Stderr = chunkWriter{stderr: true, ch: chunks}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		go func() {
			select {
			case <-exited:
			case <-time.After(KillDelay):
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}()
		return err
	}
	// After cancel or exit, stop waiting for pipes held open by leftovers.
	cmd.WaitDelay = KillDelay + time.Second

	if err := cmd.Start(); err != nil {
		code := ExitCannotExecute
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			code = ExitNotFound
		}
		msg := fmt.Sprintf("hoex: %s: %v\n", argv[0], err)
		return code, out(Chunk{Stderr: true, Data: []byte(msg)})
	}

	// Wait returns only after the copying goroutines of exec stopped writing
	// to chunks, so closing it afterwards is safe.
	go func() {
		_ = cmd.Wait() // the exit status is read from cmd.ProcessState
		close(exited)
		close(chunks)
	}()

	var outErr error
	for c := range chunks {
		if outErr != nil {
			continue // keep draining so the command is not blocked
		}
		if outErr = out(c); outErr != nil {
			cancel()
		}
	}

	if outErr != nil {
		return 0, outErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return exitCode(cmd.ProcessState), nil
}

// chunkWriter hands each write to the consumer goroutine.
type chunkWriter struct {
	stderr bool
	ch     chan<- Chunk
}

func (w chunkWriter) Write(p []byte) (int, error) {
	w.ch <- Chunk{Stderr: w.stderr, Data: append([]byte(nil), p...)}
	return len(p), nil
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return ExitCannotExecute
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	return ExitCannotExecute
}
