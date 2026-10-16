package events

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Logger writes events as plain text lines. Output lines are prefixed with
// the run ID so that concurrent runs can be told apart.
type Logger struct {
	mu      sync.Mutex
	w       io.Writer
	pending map[stream][]byte // incomplete last line per run and stream
}

type stream struct {
	run    int
	stderr bool
}

// NewLogger returns a Logger writing to w.
func NewLogger(w io.Writer) *Logger {
	return &Logger{w: w, pending: make(map[stream][]byte)}
}

// Send implements Sink.
func (l *Logger) Send(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch e := e.(type) {
	case Started:
		l.printf(e.Time, "#%d run   %s  (in %s)", e.RunID, Quote(e.Argv), e.Cwd)
	case Output:
		l.output(e)
	case Finished:
		l.flush(e.RunID)
		if e.Err != nil {
			l.printf(e.Time, "#%d abort %v", e.RunID, e.Err)
		} else {
			l.printf(e.Time, "#%d exit  %d", e.RunID, e.ExitCode)
		}
	case Rejected:
		l.printf(e.Time, "#%d deny  %s  (%s)", e.RunID, Quote(e.Argv), e.Reason)
	case HostStarted:
		for _, w := range e.Warnings {
			_, _ = fmt.Fprintln(l.w, "warning:", w)
		}
		l.printf(e.Time, "hoex host: %s, port %d, %d rules", e.Root, e.Port, e.Rules)
	case Warning:
		l.printf(e.Time, "warning: %s", e.Text)
	case Stopping:
		l.printf(e.Time, "hoex host: shutting down")
	}
}

func (l *Logger) output(e Output) {
	k := stream{e.RunID, e.Stderr}
	buf := append(l.pending[k], e.Data...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		l.line(k, buf[:i])
		buf = buf[i+1:]
	}
	if len(buf) > 0 {
		l.pending[k] = append([]byte(nil), buf...)
	} else {
		delete(l.pending, k)
	}
}

func (l *Logger) flush(run int) {
	for _, stderr := range []bool{false, true} {
		k := stream{run, stderr}
		if buf, ok := l.pending[k]; ok {
			l.line(k, buf)
			delete(l.pending, k)
		}
	}
}

func (l *Logger) line(k stream, b []byte) {
	sep := "│"
	if k.stderr {
		sep = "┃"
	}
	_, _ = fmt.Fprintf(l.w, "#%d %s %s\n", k.run, sep, bytes.TrimSuffix(b, []byte("\r")))
}

func (l *Logger) printf(t time.Time, format string, args ...any) {
	_, _ = fmt.Fprintf(l.w, "%s "+format+"\n", append([]any{t.Format("15:04:05")}, args...)...)
}

// Quote renders argv so that argument boundaries stay visible.
func Quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`;&|<>(){}*?[]#~") {
			parts[i] = fmt.Sprintf("%q", a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
