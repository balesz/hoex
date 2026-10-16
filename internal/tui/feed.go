// Package tui is the terminal interface of host mode: the list of runs,
// the live output of the selected run and the host status.
package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/balesz/hoex/internal/events"
)

// Feed is an events.Sink that buffers events for the TUI. Send never waits
// for the UI, so a slow terminal does not slow down the commands; the UI
// takes everything that piled up in one batch.
type Feed struct {
	mu      sync.Mutex
	pending []events.Event
	notify  chan struct{}
}

// NewFeed returns an empty feed.
func NewFeed() *Feed {
	return &Feed{notify: make(chan struct{}, 1)}
}

// Send implements events.Sink.
func (f *Feed) Send(e events.Event) {
	f.mu.Lock()
	f.pending = append(f.pending, e)
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
}

// batchMsg carries the events received since the last batch.
type batchMsg []events.Event

// next waits for events and returns them as one batch.
func (f *Feed) next() tea.Cmd {
	return func() tea.Msg {
		<-f.notify
		f.mu.Lock()
		defer f.mu.Unlock()
		batch := f.pending
		f.pending = nil
		return batchMsg(batch)
	}
}
