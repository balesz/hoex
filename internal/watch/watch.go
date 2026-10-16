// Package watch notices when a file may have changed. It watches the
// directory of the file, so that atomic replacement (write to a temporary
// file, then rename) is seen too, and it also checks the file at a fixed
// interval, because file events do not always cross the mount between the
// sandbox and the host.
package watch

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Options configure Watch.
type Options struct {
	// Interval is how often to check without a file event.
	Interval time.Duration
	// Settle is how long to wait after a file event before notifying, so
	// that an editor finishes writing.
	Settle time.Duration
	// OnError reports a watcher error; watching continues with the interval.
	OnError func(error)
}

// Watch calls check after every event on file and at every interval, until
// ctx is done. check runs on the calling goroutine, one call at a time; it
// should compare the content with the last one it saw.
func Watch(ctx context.Context, file string, o Options, check func()) {
	poll := time.NewTicker(o.Interval)
	defer poll.Stop()

	var fsEvents <-chan fsnotify.Event
	var fsErrors <-chan error
	if w, err := fsnotify.NewWatcher(); err != nil {
		o.onError(err)
	} else {
		defer func() { _ = w.Close() }()
		if err := w.Add(filepath.Dir(file)); err != nil {
			o.onError(err)
		} else {
			fsEvents, fsErrors = w.Events, w.Errors
		}
	}

	// settle delays the check after a burst of events.
	settle := time.NewTimer(0)
	if !settle.Stop() {
		<-settle.C
	}
	name := filepath.Base(file)
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-fsEvents:
			if !ok {
				fsEvents = nil
				continue
			}
			if filepath.Base(e.Name) == name {
				settle.Reset(o.Settle)
			}
		case err, ok := <-fsErrors:
			if !ok {
				fsErrors = nil
				continue
			}
			o.onError(err)
		case <-settle.C:
			check()
		case <-poll.C:
			check()
		}
	}
}

func (o Options) onError(err error) {
	if o.OnError != nil {
		o.OnError(err)
	}
}
