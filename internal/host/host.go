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

	"github.com/balesz/hoex/internal/audit"
	"github.com/balesz/hoex/internal/config"
	"github.com/balesz/hoex/internal/connfile"
	"github.com/balesz/hoex/internal/events"
	"github.com/balesz/hoex/internal/policy"
	"github.com/balesz/hoex/internal/server"
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
}

// Run serves until ctx is canceled, then removes the connection file.
func Run(ctx context.Context, o Options) error {
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return err
	}
	cfg, err := config.Load(root, o.UserConfig)
	if err != nil {
		return err
	}
	warnings := cfg.Warnings
	if len(cfg.Rules) == 0 {
		warnings = append(warnings, fmt.Sprintf("no allow rules in %s or the user config; every command will be rejected", cfg.ProjectFile))
	}

	port := cfg.Port
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
	log, err := audit.Open(filepath.Join(root, connfile.Dir))
	if err != nil {
		_ = lis.Close()
		return err
	}
	log.OnError = func(err error) {
		o.Display.Send(events.Warning{Time: time.Now(), Text: "audit log: " + err.Error()})
	}
	defer func() { _ = log.Close() }()
	sink := events.Multi{o.Display, log}

	srv := server.New(root, token, policy.New(cfg.Rules), sink)
	if err := connfile.Write(root, connfile.Info{Port: actual, Token: token, PID: os.Getpid()}); err != nil {
		_ = lis.Close()
		return err
	}
	defer func() { _ = connfile.Remove(root) }()

	sink.Send(events.HostStarted{Time: time.Now(), Root: root, Port: actual, Rules: len(cfg.Rules), Warnings: warnings})
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

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
