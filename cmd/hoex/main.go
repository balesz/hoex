// Command hoex lets an agent in a Docker sandbox run allow-listed commands on
// the host. See docs/PRD.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/balesz/hoex/internal/approval"
	"github.com/balesz/hoex/internal/client"
	"github.com/balesz/hoex/internal/config"
	"github.com/balesz/hoex/internal/events"
	"github.com/balesz/hoex/internal/exitcode"
	"github.com/balesz/hoex/internal/host"
	"github.com/balesz/hoex/internal/tui"
)

const usage = `usage:
  hoex --mode host [--port N] [--no-tui]  run the host daemon in the project root
  hoex --mode sandbox -- <command>...     run a command on the host from the sandbox
  hoex --skill claude                     install the agent skill into the project

exit codes in sandbox mode: the host command's own code, or
  64 usage error, 69 host unreachable, 70 internal error, 77 command rejected
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	mode := flag.String("mode", "", "host | sandbox")
	skill := flag.String("skill", "", "install the agent skill (claude)")
	port := flag.Int("port", 0, "host mode: port to listen on (default from config, else 47800)")
	noTUI := flag.Bool("no-tui", false, "host mode: plain log lines instead of the terminal UI")
	flag.Parse()

	switch {
	case *skill != "":
		fail(exitcode.Internal, "--skill is not implemented yet")
	case *mode == "host":
		os.Exit(runHost(*port, *noTUI || !isTerminal(os.Stdout)))
	case *mode == "sandbox":
		os.Exit(runSandbox(flag.Args()))
	default:
		flag.Usage()
		os.Exit(exitcode.Usage)
	}
}

func runHost(port int, plain bool) int {
	userFile, err := config.UserFilePath()
	if err != nil {
		fail(exitcode.Internal, err.Error())
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail(exitcode.Internal, err.Error())
	}
	approvedDir, err := approval.DefaultDir()
	if err != nil {
		fail(exitcode.Internal, err.Error())
	}
	opts := host.Options{Root: cwd, UserConfig: userFile, Port: port, Approvals: &approval.Store{Dir: approvedDir}}
	if plain {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		opts.Display = events.NewLogger(os.Stdout)
		err = host.Run(ctx, opts)
	} else {
		err = runHostTUI(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hoex:", err)
		return 1
	}
	return 0
}

// runHostTUI runs the daemon behind the terminal UI. Quitting the UI stops the
// daemon; a daemon that fails (e.g. invalid config) closes the UI.
func runHostTUI(opts host.Options) error {
	feed := tui.NewFeed()
	opts.Display = feed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	go func() {
		err := host.Run(ctx, opts)
		feed.Send(events.HostStopped{Err: err})
		hostErr <- err
	}()
	_, uiErr := tea.NewProgram(tui.New(feed)).Run()
	cancel()
	if err := <-hostErr; err != nil {
		return err
	}
	return uiErr
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runSandbox(argv []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fail(exitcode.Internal, err.Error())
	}
	// On a signal, cancel the call so the host stops the command, then exit
	// like a shell does for a command killed by that signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	var got atomic.Value
	go func() {
		got.Store(<-sigc)
		cancel()
	}()

	code := client.Run(ctx, client.Options{
		Argv:   argv,
		Cwd:    cwd,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if sig, ok := got.Load().(syscall.Signal); ok {
		return 128 + int(sig)
	}
	return code
}

func fail(code int, msg string) {
	fmt.Fprintln(os.Stderr, "hoex:", msg)
	os.Exit(code)
}
