// Package host implements host mode: it loads the configuration, starts the
// gRPC server on a local port and publishes the connection file.
package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/balesz/hoex/internal/approval"
	"github.com/balesz/hoex/internal/audit"
	"github.com/balesz/hoex/internal/config"
	"github.com/balesz/hoex/internal/connfile"
	"github.com/balesz/hoex/internal/events"
	"github.com/balesz/hoex/internal/policy"
	"github.com/balesz/hoex/internal/server"
	"github.com/balesz/hoex/internal/skill"
	"github.com/balesz/hoex/internal/watch"
)

// Options configure Run.
type Options struct {
	// Root is the project root; the daemon runs commands there.
	Root string
	// UserConfig is the user-level config file; empty skips it.
	UserConfig string
	// Port overrides the configured port when non-zero.
	Port int
	// Listener, if set, is used instead of listening on the port (tests).
	Listener net.Listener
	// Display shows what happens: the line logger or the TUI.
	Display events.Sink
	// Ready, if set, is called with the listening address once serving.
	Ready func(addr net.Addr)
	// Approvals remembers the approved project config between runs; nil
	// trusts the file at every start (tests).
	Approvals *approval.Store
	// WatchInterval is how often the project config is checked for changes
	// besides file events; zero means 2s.
	WatchInterval time.Duration
}

// Run serves until ctx is canceled, then removes the connection file.
func Run(ctx context.Context, o Options) error {
	root, err := resolveRoot(o.Root)
	if err != nil {
		return err
	}
	data, err := config.ReadProjectFile(root)
	if err != nil {
		return err
	}
	cfg, err := config.LoadData(root, o.UserConfig, data)
	if err != nil {
		return err
	}
	// active is the configuration in effect: the last approved one. If the
	// file changed since, the change is offered for approval right away.
	active, err := approvedConfig(root, o, cfg, data)
	if err != nil {
		return err
	}
	port := active.Port
	if o.Port != 0 {
		port = o.Port
	}
	lis := o.Listener
	if lis == nil {
		lis, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %d is already in use; is another hoex host running? Set another port with --port or the port key in %s",
				port, config.ProjectFileName)
		}
		if err != nil {
			return err
		}
	}
	actual := lis.Addr().(*net.TCPAddr).Port

	token, err := newToken()
	if err != nil {
		return err
	}
	if err := connfile.EnsureDir(root); err != nil {
		_ = lis.Close()
		return err
	}
	log, err := audit.Open(root)
	if err != nil {
		_ = lis.Close()
		return err
	}
	log.OnError = func(err error) {
		o.Display.Send(events.Warning{Time: time.Now(), Text: "audit log: " + err.Error()})
	}
	defer func() { _ = log.Close() }()
	sink := events.Multi{o.Display, log}

	srv := server.New(root, token, policy.New(active.Rules), sink)
	if err := connfile.Write(root, connfile.Info{Port: actual, Token: token, PID: os.Getpid()}); err != nil {
		_ = lis.Close()
		return err
	}
	defer func() { _ = connfile.Remove(root) }()

	updateSkill(root, active, sink)
	sink.Send(events.HostStarted{
		Time: time.Now(), Root: root, Port: actual, Rules: len(active.Rules),
		Checksum: active.Checksum, Warnings: configWarnings(active),
	})

	// The project config file is not trusted: a change only takes effect
	// once the user approves it.
	rl := &reloader{
		root: root, userFile: o.UserConfig, sink: sink,
		active: active, seen: active.Checksum,
		apply: func(c *config.Config) {
			srv.SetPolicy(policy.New(c.Rules))
			updateSkill(root, c, sink)
		},
		approved: func(data []byte) {
			if o.Approvals == nil {
				return
			}
			if err := o.Approvals.Save(root, data); err != nil {
				sink.Send(events.Warning{Time: time.Now(), Text: "saving the approved config: " + err.Error()})
			}
		},
	}
	rl.check()
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		interval := o.WatchInterval
		if interval == 0 {
			interval = 2 * time.Second
		}
		watch.Watch(watchCtx, cfg.ProjectFile, watch.Options{
			Interval: interval,
			Settle:   100 * time.Millisecond,
			OnError: func(err error) {
				sink.Send(events.Warning{Time: time.Now(), Text: "watching the config: " + err.Error()})
			},
		}, rl.check)
	}()
	defer func() {
		stopWatch()
		<-watchDone
	}()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(lis) }()
	if o.Ready != nil {
		o.Ready(lis.Addr())
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sink.Send(events.Stopping{Time: time.Now()})
		srv.Stop(5 * time.Second)
		return nil
	}
}

func resolveRoot(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

// updateSkill regenerates the agent skills installed in the project so that
// they describe the active rules.
func updateSkill(root string, cfg *config.Config, sink events.Sink) {
	if _, err := skill.Update(root, cfg); err != nil {
		sink.Send(events.Warning{Time: time.Now(), Text: "updating the agent skill: " + err.Error()})
	}
}

// Active is the configuration a host would start with in a project.
type Active struct {
	Root   string
	Config *config.Config
	// Pending is set if the project file differs from the approved content.
	Pending bool
}

// ActiveConfig returns the configuration in effect for the project at dir:
// the last approved project config, or the current file if none was
// approved yet. Unlike Run, it records nothing.
func ActiveConfig(dir, userFile string, approvals *approval.Store) (*Active, error) {
	root, err := resolveRoot(dir)
	if err != nil {
		return nil, err
	}
	data, err := config.ReadProjectFile(root)
	if err != nil {
		return nil, err
	}
	if approvals != nil {
		approved, ok, err := approvals.Load(root)
		if err != nil {
			return nil, err
		}
		if ok {
			if cfg, err := config.LoadData(root, userFile, approved); err == nil {
				return &Active{Root: root, Config: cfg, Pending: config.Checksum(approved) != config.Checksum(data)}, nil
			}
		}
	}
	cfg, err := config.LoadData(root, userFile, data)
	if err != nil {
		return nil, err
	}
	return &Active{Root: root, Config: cfg}, nil
}

// approvedConfig returns the configuration from the last approved content of
// the project file. On the first start in a project the current file counts
// as approved: the user just wrote it and started the host.
func approvedConfig(root string, o Options, current *config.Config, data []byte) (*config.Config, error) {
	if o.Approvals == nil {
		return current, nil
	}
	approved, ok, err := o.Approvals.Load(root)
	if err != nil {
		return nil, fmt.Errorf("reading the approved config: %w", err)
	}
	if !ok {
		if err := o.Approvals.Save(root, data); err != nil {
			return nil, fmt.Errorf("saving the approved config: %w", err)
		}
		return current, nil
	}
	if config.Checksum(approved) == current.Checksum {
		return current, nil
	}
	cfg, err := config.LoadData(root, o.UserConfig, approved)
	if err != nil {
		// The approved content no longer loads (e.g. the user config
		// changed); start from the user-level rules alone.
		return config.LoadData(root, o.UserConfig, nil)
	}
	return cfg, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
