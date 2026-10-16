// Package client implements sandbox mode: it sends a command to the host
// daemon and replays its output and exit code.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	hoexv1 "github.com/balesz/hoex/gen/hoex/v1"
	"github.com/balesz/hoex/internal/connfile"
	"github.com/balesz/hoex/internal/exitcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// DefaultHost is how a Docker container reaches the host.
const DefaultHost = "host.docker.internal"

// HostEnv overrides DefaultHost, e.g. for tests or running outside Docker.
const HostEnv = "HOEX_HOST"

// Options configure Run.
type Options struct {
	Argv []string
	// Cwd is sent to the host and used to find .hoex/connection.
	Cwd string
	// Host is the host name of the daemon; empty means $HOEX_HOST or DefaultHost.
	Host   string
	Stdout io.Writer
	Stderr io.Writer
}

// Run sends the command and returns the exit code for the sandbox process:
// the host command's own code, or one of the exitcode constants.
func Run(ctx context.Context, o Options) int {
	if len(o.Argv) == 0 {
		o.errorf("no command given; usage: hoex --mode sandbox -- <command> [args...]")
		return exitcode.Usage
	}
	root, info, err := connfile.Find(o.Cwd)
	if errors.Is(err, connfile.ErrNotFound) {
		o.errorf("no %s/%s found in %s or its parents.\nStart the host daemon in the project root on the host: hoex --mode host",
			connfile.Dir, connfile.FileName, o.Cwd)
		return exitcode.Unavailable
	}
	if err != nil {
		o.errorf("%v", err)
		return exitcode.Unavailable
	}

	host := o.Host
	if host == "" {
		host = os.Getenv(HostEnv)
	}
	if host == "" {
		host = DefaultHost
	}
	addr := net.JoinHostPort(host, strconv.Itoa(info.Port))

	conn, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		o.errorf("%v", err)
		return exitcode.Internal
	}
	defer func() { _ = conn.Close() }()

	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+info.Token)
	stream, err := hoexv1.NewHostRunnerClient(conn).Run(ctx, &hoexv1.RunRequest{Argv: o.Argv, Cwd: o.Cwd})
	if err != nil {
		return o.rpcError(err, root, addr, info.Port)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				o.errorf("the host closed the stream without an exit code")
				return exitcode.Internal
			}
			return o.rpcError(err, root, addr, info.Port)
		}
		switch e := ev.GetEvent().(type) {
		case *hoexv1.RunEvent_Stdout:
			if _, err := o.Stdout.Write(e.Stdout); err != nil {
				// The reader went away (e.g. `| head`); stop like a shell would.
				return exitcode.Internal
			}
		case *hoexv1.RunEvent_Stderr:
			_, _ = o.Stderr.Write(e.Stderr)
		case *hoexv1.RunEvent_ExitCode:
			return int(e.ExitCode)
		case *hoexv1.RunEvent_Error:
			o.errorf("rejected by the host: %s", e.Error)
			return exitcode.Denied
		}
	}
}

func (o Options) rpcError(err error, root, addr string, port int) int {
	switch status.Code(err) {
	case codes.Unavailable:
		o.errorf("cannot reach the host daemon at %s.\n"+
			"  - Is `hoex --mode host` running in %s on the host?\n"+
			"  - Is the port allowed in the sandbox network policy? On the host: sbx policy allow network localhost:%d",
			addr, root, port)
		return exitcode.Unavailable
	case codes.Unauthenticated:
		o.errorf("the daemon at %s rejected the token in %s.\n"+
			"  Another project's hoex host may be using port %d; give each project its own port.",
			addr, connfile.Path(root), port)
		return exitcode.Unavailable
	case codes.Canceled:
		return exitcode.Internal
	}
	o.errorf("%v", err)
	return exitcode.Internal
}

func (o Options) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.Stderr, "hoex: "+format+"\n", args...)
}
