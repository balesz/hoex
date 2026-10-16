// Command hoex-probe checks whether a sandbox can reach a gRPC server on the
// host (milestone M0). It speaks the real HostRunner protocol but runs no
// commands: the server answers with synthetic output.
//
//	host:    hoex-probe server --listen 127.0.0.1:47800
//	sandbox: hoex-probe client --addr host.docker.internal:47800 --token <token>
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	hoexv1 "github.com/balesz/hoex/gen/hoex/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "client":
		os.Exit(runClient(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hoex-probe server [--listen addr]... | client --addr host:port --token t [--proxy auto|off]")
	os.Exit(64)
}

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// --- server ---------------------------------------------------------------

type server struct {
	hoexv1.UnimplementedHostRunnerServer
	token string
}

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	var listens listFlag
	fs.Var(&listens, "listen", "address to listen on (repeatable, default 127.0.0.1:0)")
	token := fs.String("token", "", "auth token (default: random)")
	_ = fs.Parse(args)
	if len(listens) == 0 {
		listens = listFlag{"127.0.0.1:0"}
	}
	if *token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatal(err)
		}
		*token = hex.EncodeToString(b)
	}

	s := &server{token: *token}
	gs := grpc.NewServer(grpc.StreamInterceptor(s.auth))
	hoexv1.RegisterHostRunnerServer(gs, s)

	errc := make(chan error, len(listens))
	for _, addr := range listens {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("listen %s: %v", addr, err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("listening on %s", lis.Addr())
		log.Printf("  sandbox: hoex-probe client --addr host.docker.internal:%d --token %s", port, *token)
		go func() { errc <- gs.Serve(lis) }()
	}
	log.Fatal(<-errc)
}

func (s *server) auth(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	md, _ := metadata.FromIncomingContext(ss.Context())
	got := strings.TrimPrefix(first(md.Get("authorization")), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		log.Printf("rejected: bad token")
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return handler(srv, ss)
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// Run answers three synthetic commands:
//
//	echo <args...>  stdout + stderr + exit 3
//	bulk <MiB>      that many MiB of stdout in 32 KiB chunks
//	ticks           one stdout line per 200ms until the client cancels
func (s *server) Run(req *hoexv1.RunRequest, stream grpc.ServerStreamingServer[hoexv1.RunEvent]) error {
	log.Printf("run argv=%q cwd=%q", req.GetArgv(), req.GetCwd())
	argv := req.GetArgv()
	if len(argv) == 0 {
		return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_Error{Error: "empty argv"}})
	}
	switch argv[0] {
	case "echo":
		if err := sendOut(stream, []byte(strings.Join(argv[1:], " ")+"\n")); err != nil {
			return err
		}
		if err := stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_Stderr{Stderr: []byte("to stderr\n")}}); err != nil {
			return err
		}
		return sendExit(stream, 3)
	case "bulk":
		mib, _ := strconv.Atoi(first(argv[1:]))
		chunk := make([]byte, 32<<10)
		for i := range chunk {
			chunk[i] = 'a' + byte(i%26)
		}
		for i := 0; i < mib*32; i++ {
			if err := sendOut(stream, chunk); err != nil {
				return err
			}
		}
		return sendExit(stream, 0)
	case "ticks":
		for i := 0; ; i++ {
			select {
			case <-stream.Context().Done():
				log.Printf("ticks: client canceled after %d ticks (OK)", i)
				return stream.Context().Err()
			case <-time.After(200 * time.Millisecond):
			}
			if err := sendOut(stream, fmt.Appendf(nil, "tick %d\n", i)); err != nil {
				return err
			}
		}
	default:
		return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_Error{Error: "unknown probe command: " + argv[0]}})
	}
}

func sendOut(stream grpc.ServerStreamingServer[hoexv1.RunEvent], b []byte) error {
	return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_Stdout{Stdout: b}})
}

func sendExit(stream grpc.ServerStreamingServer[hoexv1.RunEvent], code int32) error {
	return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_ExitCode{ExitCode: code}})
}

// --- client ---------------------------------------------------------------

func runClient(args []string) int {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	addr := fs.String("addr", "", "host:port of the probe server")
	token := fs.String("token", "", "auth token printed by the server")
	proxy := fs.String("proxy", "auto", "auto: honour HTTPS_PROXY; off: dial directly")
	timeout := fs.Duration("timeout", 10*time.Second, "per-test timeout")
	_ = fs.Parse(args)
	if *addr == "" || *token == "" {
		usage()
	}

	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if *proxy == "off" {
		opts = append(opts, grpc.WithNoProxy())
	}
	conn, err := grpc.NewClient("passthrough:///"+*addr, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		return 69
	}
	defer func() { _ = conn.Close() }()
	c := hoexv1.NewHostRunnerClient(conn)

	tests := []struct {
		name string
		fn   func(context.Context, hoexv1.HostRunnerClient, string) error
	}{
		{"bad token rejected", testBadToken},
		{"echo: stdout, stderr, exit code", testEcho},
		{"bulk: 16 MiB streamed", testBulk},
		{"ticks: cancel mid-stream", testCancel},
	}
	failed := 0
	for _, t := range tests {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		start := time.Now()
		err := t.fn(ctx, c, *token)
		cancel()
		if err != nil {
			failed++
			fmt.Printf("FAIL  %-34s %v\n", t.name, err)
		} else {
			fmt.Printf("ok    %-34s %v\n", t.name, time.Since(start).Round(time.Millisecond))
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func authed(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// collect drains a Run stream.
func collect(ctx context.Context, c hoexv1.HostRunnerClient, token string, argv ...string) (stdout, stderr []byte, exit int32, err error) {
	stream, err := c.Run(authed(ctx, token), &hoexv1.RunRequest{Argv: argv, Cwd: "/probe"})
	if err != nil {
		return nil, nil, 0, err
	}
	exit = -1
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stdout, stderr, exit, nil
		}
		if err != nil {
			return stdout, stderr, exit, err
		}
		switch e := ev.GetEvent().(type) {
		case *hoexv1.RunEvent_Stdout:
			stdout = append(stdout, e.Stdout...)
		case *hoexv1.RunEvent_Stderr:
			stderr = append(stderr, e.Stderr...)
		case *hoexv1.RunEvent_ExitCode:
			exit = e.ExitCode
		case *hoexv1.RunEvent_Error:
			return stdout, stderr, exit, fmt.Errorf("server error: %s", e.Error)
		}
	}
}

func testBadToken(ctx context.Context, c hoexv1.HostRunnerClient, _ string) error {
	_, _, _, err := collect(ctx, c, "wrong", "echo", "x")
	if status.Code(err) != codes.Unauthenticated {
		return fmt.Errorf("want Unauthenticated, got %v", err)
	}
	return nil
}

func testEcho(ctx context.Context, c hoexv1.HostRunnerClient, token string) error {
	out, errOut, exit, err := collect(ctx, c, token, "echo", "hello", "from host")
	if err != nil {
		return err
	}
	if string(out) != "hello from host\n" || string(errOut) != "to stderr\n" || exit != 3 {
		return fmt.Errorf("unexpected result: stdout=%q stderr=%q exit=%d", out, errOut, exit)
	}
	return nil
}

func testBulk(ctx context.Context, c hoexv1.HostRunnerClient, token string) error {
	out, _, exit, err := collect(ctx, c, token, "bulk", "16")
	if err != nil {
		return err
	}
	if len(out) != 16<<20 || exit != 0 {
		return fmt.Errorf("got %d bytes, exit %d", len(out), exit)
	}
	return nil
}

func testCancel(ctx context.Context, c hoexv1.HostRunnerClient, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 1100*time.Millisecond)
	defer cancel()
	out, _, _, err := collect(ctx, c, token, "ticks")
	if status.Code(err) != codes.DeadlineExceeded {
		return fmt.Errorf("want DeadlineExceeded, got %v", err)
	}
	if !strings.HasPrefix(string(out), "tick 0\n") {
		return fmt.Errorf("no ticks received before cancel: %q", out)
	}
	return nil // server log should show "client canceled"
}
