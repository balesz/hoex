// Package runner starts a command on the host without a shell and streams
// its output.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
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
func Run(ctx context.Context, argv []string, dir string, out OutputFunc) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		code := ExitCannotExecute
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			code = ExitNotFound
		}
		msg := fmt.Sprintf("hoex: %s: %v\n", argv[0], err)
		return code, out(Chunk{Stderr: true, Data: []byte(msg)})
	}

	chunks := make(chan Chunk)
	done := make(chan struct{}, 2)
	go pump(stdout, false, chunks, done)
	go pump(stderr, true, chunks, done)

	var outErr error
	for open := 2; open > 0; {
		select {
		case c := <-chunks:
			if outErr == nil {
				if outErr = out(c); outErr != nil {
					cancel()
				}
			}
		case <-done:
			open--
		}
	}
	_ = cmd.Wait() // the exit status is read from cmd.ProcessState

	if outErr != nil {
		return 0, outErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return exitCode(cmd.ProcessState), nil
}

// pump reads r until EOF and sends copies of what it read to chunks.
func pump(r io.Reader, isStderr bool, chunks chan<- Chunk, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunks <- Chunk{Stderr: isStderr, Data: append([]byte(nil), buf[:n]...)}
		}
		if err != nil {
			return
		}
	}
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
