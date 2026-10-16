package tui

import (
	"bytes"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/balesz/hoex/internal/events"
)

// maxLines is how many output lines are kept per run; older lines are
// dropped (the full output went to the sandbox anyway).
const maxLines = 10000

type state int

const (
	running state = iota
	exited
	aborted
	denied
)

type line struct {
	stderr bool
	text   string
}

// run is one request from the sandbox as shown in the TUI.
type run struct {
	id         int
	argv       []string
	cwd        string
	start, end time.Time
	state      state
	exitCode   int
	reason     string // denied: why; aborted: what stopped it

	lines   []line
	dropped int
	partial [2][]byte // incomplete last line of stdout and stderr
	version int       // increments whenever lines change
}

func (r *run) command() string {
	if len(r.argv) == 0 {
		return "(no command)"
	}
	return events.Quote(r.argv)
}

// write adds output, splitting it into lines.
func (r *run) write(stderr bool, data []byte) {
	i := 0
	if stderr {
		i = 1
	}
	buf := append(r.partial[i], data...)
	for {
		n := bytes.IndexByte(buf, '\n')
		if n < 0 {
			break
		}
		r.add(stderr, buf[:n])
		buf = buf[n+1:]
	}
	r.partial[i] = append(r.partial[i][:0], buf...)
	r.version++
}

// flush adds the incomplete last lines, at the end of a run.
func (r *run) flush() {
	for i, buf := range r.partial {
		if len(buf) > 0 {
			r.add(i == 1, buf)
			r.partial[i] = nil
		}
	}
}

func (r *run) add(stderr bool, b []byte) {
	r.lines = append(r.lines, line{stderr: stderr, text: sanitize(string(b))})
	if len(r.lines) > maxLines {
		drop := len(r.lines) - maxLines
		r.lines = append(r.lines[:0:0], r.lines[drop:]...)
		r.dropped += drop
	}
	r.version++
}

// sanitize makes a line of command output safe to draw: it keeps what a
// terminal would show after carriage returns (progress bars), and removes
// escape sequences and control characters that would break the layout.
func sanitize(s string) string {
	s = strings.TrimSuffix(s, "\r")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
