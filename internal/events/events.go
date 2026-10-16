// Package events carries what happens on the host (runs, output, rejections)
// to whatever displays or records it: the line logger, the TUI and the audit
// log.
package events

import (
	"time"
)

// Event is one of the types below.
type Event interface{ isEvent() }

// Started is sent when a command is allowed and about to start.
type Started struct {
	RunID int
	Time  time.Time
	Argv  []string
	Cwd   string
}

// Output is a chunk of command output.
type Output struct {
	RunID  int
	Stderr bool
	Data   []byte
}

// Finished is sent when a started command ended.
type Finished struct {
	RunID    int
	Time     time.Time
	ExitCode int
	// Err is set if the run was aborted (e.g. the sandbox disconnected).
	Err error
}

// Rejected is sent when a request did not pass the checks.
type Rejected struct {
	RunID  int
	Time   time.Time
	Argv   []string
	Cwd    string
	Reason string
}

// HostStarted is sent once the daemon listens.
type HostStarted struct {
	Time     time.Time
	Root     string
	Port     int
	Rules    int
	Warnings []string
}

// Warning is a non-fatal problem while running, e.g. the audit log failed.
type Warning struct {
	Time time.Time
	Text string
}

// Stopping is sent when the daemon starts shutting down.
type Stopping struct {
	Time time.Time
}

// HostStopped is sent after the daemon stopped; Err is why, if it failed.
type HostStopped struct {
	Err error
}

func (Started) isEvent()     {}
func (Output) isEvent()      {}
func (Finished) isEvent()    {}
func (Rejected) isEvent()    {}
func (HostStarted) isEvent() {}
func (Warning) isEvent()     {}
func (Stopping) isEvent()    {}
func (HostStopped) isEvent() {}

// Sink receives events. Send may be called from several goroutines.
type Sink interface {
	Send(Event)
}

// Multi sends every event to all sinks.
type Multi []Sink

// Send implements Sink.
func (m Multi) Send(e Event) {
	for _, s := range m {
		s.Send(e)
	}
}
