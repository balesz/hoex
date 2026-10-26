package tui

import (
	"os"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/balesz/hoex/internal/events"
)

// Model is the Bubble Tea model of host mode.
type Model struct {
	feed    *Feed
	now     func() time.Time
	homeDir string

	width, height int

	host *events.HostStarted
	// cfgWarnings are about the active configuration; warning is the
	// latest problem while running.
	cfgWarnings []string
	warning     *events.Warning
	stopping    bool
	// change is the config change waiting for approval.
	change *events.ConfigChanged

	runs []*run
	byID map[int]*run

	// sel is the index of the selected run in runs, -1 if none.
	sel int
	// follow selects every new run as it arrives.
	follow bool
	// listTop is the first run shown in the list.
	listTop int

	vp        viewport.Model
	vpRun     *run // run whose output is in the viewport
	vpVersion int  // its version when the viewport was filled
}

// New returns the model reading events from feed.
func New(feed *Feed) Model {
	home, _ := os.UserHomeDir()
	return Model{
		feed:    feed,
		now:     time.Now,
		homeDir: home,
		byID:    make(map[int]*run),
		sel:     -1,
		follow:  true,
		vp:      viewport.New(),
	}
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.feed.next(), tick())
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case batchMsg:
		quit := false
		for _, e := range msg {
			if m.apply(e) {
				quit = true
			}
		}
		m.refresh()
		if quit {
			return m, tea.Quit
		}
		return m, m.feed.next()

	case tickMsg:
		// Redraw the running time of active runs.
		return m, tick()

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// apply updates the model with one event and reports whether the UI should
// quit because the host stopped.
func (m *Model) apply(e events.Event) bool {
	switch e := e.(type) {
	case events.HostStarted:
		m.host = &e
		m.cfgWarnings = e.Warnings
		m.layout()
	case events.Warning:
		m.warning = &e
		m.layout()
	case events.ConfigChanged:
		m.change = &e
		m.layout()
	case events.ConfigResolved:
		if m.change != nil && m.change.ID == e.ID {
			m.change = nil
		}
		if m.host != nil {
			m.host.Rules = e.Rules
		}
		m.cfgWarnings = e.Warnings
		m.layout()
	case events.Stopping:
		m.stopping = true
	case events.HostStopped:
		return true
	case events.Started:
		m.addRun(&run{id: e.RunID, argv: e.Argv, cwd: e.Cwd, start: e.Time, state: running})
	case events.Rejected:
		m.addRun(&run{id: e.RunID, argv: e.Argv, cwd: e.Cwd, start: e.Time, end: e.Time, state: denied, reason: e.Reason})
	case events.Output:
		if r := m.byID[e.RunID]; r != nil {
			r.write(e.Stderr, e.Data)
		}
	case events.Finished:
		if r := m.byID[e.RunID]; r != nil {
			r.flush()
			r.end = e.Time
			if e.Err != nil {
				r.state, r.reason = aborted, e.Err.Error()
			} else {
				r.state, r.exitCode = exited, e.ExitCode
			}
		}
	}
	return false
}

func (m *Model) addRun(r *run) {
	m.runs = append(m.runs, r)
	m.byID[r.id] = r
	if m.follow || m.sel < 0 {
		m.sel = len(m.runs) - 1
	}
	m.scrollList()
}

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "a", "r":
		if m.change != nil {
			// The host confirms with ConfigResolved; hide the change now so
			// that a second key press does nothing.
			m.change.Decide(msg.String() == "a")
			m.change = nil
			m.layout()
		}
	case "up", "k":
		if m.sel > 0 {
			m.sel--
			m.follow = false
		}
	case "down", "j":
		if m.sel < len(m.runs)-1 {
			m.sel++
			m.follow = m.sel == len(m.runs)-1
		}
	case "f", "end":
		m.follow = true
		m.sel = len(m.runs) - 1
	case "pgup":
		m.vp.PageUp()
	case "pgdown":
		m.vp.PageDown()
	case "ctrl+u":
		m.vp.HalfPageUp()
	case "ctrl+d":
		m.vp.HalfPageDown()
	}
	m.scrollList()
	m.refresh()
	return m, nil
}

func (m *Model) selected() *run {
	if m.sel < 0 || m.sel >= len(m.runs) {
		return nil
	}
	return m.runs[m.sel]
}

// refresh refills the viewport if the selected run or its output changed.
// It keeps following the end of the output unless the user scrolled up.
func (m *Model) refresh() {
	r := m.selected()
	if r == nil {
		return
	}
	switched := r != m.vpRun
	if !switched && r.version == m.vpVersion {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContentLines(renderLines(r))
	m.vpRun, m.vpVersion = r, r.version
	if switched || atBottom {
		m.vp.GotoBottom()
	}
}

// scrollList keeps the selected run visible in the list.
func (m *Model) scrollList() {
	h := m.listHeight()
	if h <= 0 {
		return
	}
	if m.sel < m.listTop {
		m.listTop = m.sel
	}
	if m.sel >= m.listTop+h {
		m.listTop = m.sel - h + 1
	}
	if m.listTop < 0 {
		m.listTop = 0
	}
}
