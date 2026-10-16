// Package audit appends every run and rejection to .hoex/hoex.log, one JSON
// object per line. Command output is not logged. The file outlives the host
// daemon.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/balesz/hoex/internal/events"
)

// FileName is the log file inside the .hoex directory.
const FileName = "hoex.log"

// Record is one line of the log.
type Record struct {
	Time time.Time `json:"time"`
	// Event is "start", "stop", "run", "deny", "config_change" or
	// "config_<outcome>" (config_approved, config_rejected, config_withdrawn).
	Event string   `json:"event"`
	Run   int      `json:"run,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Cwd   string   `json:"cwd,omitempty"`
	// ExitCode is set for a run that finished.
	ExitCode *int `json:"exit_code,omitempty"`
	// Aborted is set for a run that was stopped, e.g. by the sandbox.
	Aborted    string `json:"aborted,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Details of start: listening port and number of rules.
	Port  int `json:"port,omitempty"`
	Rules int `json:"rules,omitempty"`
	// Config changes: the change number, the project file checksum and the
	// difference to the active rules.
	Change   int      `json:"change,omitempty"`
	Checksum string   `json:"checksum,omitempty"`
	Diff     []string `json:"diff,omitempty"`
}

// Log is an events.Sink that writes the audit log.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	enc     *json.Encoder
	running map[int]events.Started
	// OnError is called when a record cannot be written.
	OnError func(error)
}

// Open opens (or creates) the log in dir for appending.
func Open(dir string) (*Log, error) {
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	return &Log{f: f, enc: json.NewEncoder(f), running: make(map[int]events.Started)}, nil
}

// Close records that the host daemon stopped and closes the file.
func (l *Log) Close() error {
	l.write(Record{Time: time.Now(), Event: "stop"})
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Send implements events.Sink.
func (l *Log) Send(e events.Event) {
	switch e := e.(type) {
	case events.HostStarted:
		l.write(Record{Time: e.Time, Event: "start", Port: e.Port, Rules: e.Rules, Checksum: e.Checksum})
	case events.ConfigChanged:
		l.write(Record{Time: e.Time, Event: "config_change", Change: e.ID, Checksum: e.Checksum, Diff: e.Diff})
	case events.ConfigResolved:
		l.write(Record{Time: e.Time, Event: "config_" + e.Outcome, Change: e.ID, Rules: e.Rules})
	case events.Started:
		l.mu.Lock()
		l.running[e.RunID] = e
		l.mu.Unlock()
	case events.Finished:
		l.mu.Lock()
		s, ok := l.running[e.RunID]
		delete(l.running, e.RunID)
		l.mu.Unlock()
		if !ok {
			return
		}
		r := Record{
			Time: s.Time, Event: "run", Run: e.RunID, Argv: s.Argv, Cwd: s.Cwd,
			DurationMS: e.Time.Sub(s.Time).Milliseconds(),
		}
		if e.Err != nil {
			r.Aborted = e.Err.Error()
		} else {
			code := e.ExitCode
			r.ExitCode = &code
		}
		l.write(r)
	case events.Rejected:
		l.write(Record{Time: e.Time, Event: "deny", Run: e.RunID, Argv: e.Argv, Cwd: e.Cwd, Reason: e.Reason})
	}
}

func (l *Log) write(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(r); err != nil && l.OnError != nil {
		l.OnError(err)
	}
}
