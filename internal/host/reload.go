package host

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/balesz/hoex/internal/config"
	"github.com/balesz/hoex/internal/events"
)

// reloader keeps the active configuration. When the project file changes,
// it shows the difference and waits for the user's decision; until then the
// active rules stay in effect.
type reloader struct {
	root, userFile string
	sink           events.Sink
	apply          func(*config.Config)
	// approved is called with the content of every configuration that
	// becomes active.
	approved func(data []byte)

	mu      sync.Mutex
	active  *config.Config
	seen    string // checksum of the content looked at last
	readErr string // last read error, reported once
	pending *change
	nextID  int
}

type change struct {
	id   int
	cfg  *config.Config
	data []byte
}

// check looks at the project file and reports a change in it.
func (r *reloader) check() {
	data, err := config.ReadProjectFile(r.root)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if err.Error() != r.readErr {
			r.readErr = err.Error()
			r.warn("cannot read %s, the active rules stay in effect: %v", config.ProjectFileName, err)
		}
		return
	}
	r.readErr = ""
	sum := config.Checksum(data)
	if sum == r.seen {
		return
	}
	r.seen = sum
	r.withdraw()
	if sum == r.active.Checksum {
		return // changed back
	}

	cfg, err := config.LoadData(r.root, r.userFile, data)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), "\n", "; ")
		r.warn("%s changed but is invalid, the active rules stay in effect: %s", config.ProjectFileName, msg)
		return
	}
	diff := config.Diff(r.active, cfg)
	if len(diff) == 0 {
		// Only comments, formatting or line numbers changed.
		r.active = cfg
		r.apply(cfg)
		r.approved(data)
		return
	}
	r.nextID++
	id := r.nextID
	r.pending = &change{id: id, cfg: cfg, data: data}
	r.sink.Send(events.ConfigChanged{
		ID: id, Time: time.Now(), File: cfg.ProjectFile, Checksum: sum, Diff: diff,
		Decide: func(approve bool) { r.decide(id, approve) },
	})
}

// decide applies or drops the pending change with the given id.
func (r *reloader) decide(id int, approve bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil || r.pending.id != id {
		return
	}
	outcome := events.ConfigRejected
	if approve {
		outcome = events.ConfigApproved
		r.active = r.pending.cfg
		r.apply(r.active)
		r.approved(r.pending.data)
	}
	r.pending = nil
	r.resolved(id, outcome)
}

// withdraw drops the pending change because the file changed again.
func (r *reloader) withdraw() {
	if r.pending != nil {
		id := r.pending.id
		r.pending = nil
		r.resolved(id, events.ConfigWithdrawn)
	}
}

func (r *reloader) resolved(id int, outcome string) {
	r.sink.Send(events.ConfigResolved{
		ID: id, Time: time.Now(), Outcome: outcome,
		Rules: len(r.active.Rules), Warnings: configWarnings(r.active),
	})
}

func (r *reloader) warn(format string, args ...any) {
	r.sink.Send(events.Warning{Time: time.Now(), Text: fmt.Sprintf(format, args...)})
}

// configWarnings are the warnings about a configuration shown in the header.
func configWarnings(cfg *config.Config) []string {
	w := append([]string(nil), cfg.Warnings...)
	if len(cfg.Rules) == 0 {
		w = append(w, fmt.Sprintf("no allow rules in %s or the user config; every command will be rejected", cfg.ProjectFile))
	}
	return w
}
