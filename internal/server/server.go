// Package server implements the HostRunner gRPC service on the host.
package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	hoexv1 "github.com/balesz/hoex/gen/hoex/v1"
	"github.com/balesz/hoex/internal/events"
	"github.com/balesz/hoex/internal/policy"
	"github.com/balesz/hoex/internal/runner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// errCanceled is reported when the sandbox side went away during a run.
var errCanceled = errors.New("canceled by the sandbox")

// Server runs allowed commands in the project root for the sandbox.
type Server struct {
	hoexv1.UnimplementedHostRunnerServer

	root   string
	token  string
	policy atomic.Pointer[policy.Policy]
	sink   events.Sink
	nextID atomic.Int64
	grpc   *grpc.Server
}

// New returns a server for the project at root. root must be absolute with
// symlinks resolved.
func New(root, token string, p *policy.Policy, sink events.Sink) *Server {
	s := &Server{root: root, token: token, sink: sink}
	s.policy.Store(p)
	s.grpc = grpc.NewServer(grpc.StreamInterceptor(s.auth))
	hoexv1.RegisterHostRunnerServer(s.grpc, s)
	return s
}

// SetPolicy replaces the rules for requests that arrive from now on.
func (s *Server) SetPolicy(p *policy.Policy) {
	s.policy.Store(p)
}

// Serve accepts connections on lis until Stop is called.
func (s *Server) Serve(lis net.Listener) error {
	return s.grpc.Serve(lis)
}

// Stop lets running commands finish for up to timeout, then cancels them.
func (s *Server) Stop(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.grpc.Stop()
	}
}

// auth rejects calls without the token in the "authorization" metadata.
func (s *Server) auth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	md, _ := metadata.FromIncomingContext(ss.Context())
	var got string
	if v := md.Get("authorization"); len(v) > 0 {
		got = strings.TrimPrefix(v[0], "Bearer ")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		s.sink.Send(events.Rejected{RunID: s.id(), Time: time.Now(), Reason: "invalid token"})
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return handler(srv, ss)
}

func (s *Server) id() int { return int(s.nextID.Add(1)) }

// Run checks the request and runs the command. A rejected request gets an
// error event; otherwise the stream ends with the exit code.
func (s *Server) Run(req *hoexv1.RunRequest, stream grpc.ServerStreamingServer[hoexv1.RunEvent]) error {
	id := s.id()
	argv := req.GetArgv()

	reject := func(reason string) error {
		s.sink.Send(events.Rejected{RunID: id, Time: time.Now(), Argv: argv, Cwd: req.GetCwd(), Reason: reason})
		return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_Error{Error: reason}})
	}

	dir, err := s.workDir(req.GetCwd())
	if err != nil {
		return reject(err.Error())
	}
	if d := s.policy.Load().Check(argv); !d.Allowed {
		return reject(d.Reason)
	}

	s.sink.Send(events.Started{RunID: id, Time: time.Now(), Argv: argv, Cwd: dir})
	code, err := runner.Run(stream.Context(), argv, dir, func(c runner.Chunk) error {
		s.sink.Send(events.Output{RunID: id, Stderr: c.Stderr, Data: c.Data})
		ev := &hoexv1.RunEvent{Event: &hoexv1.RunEvent_Stdout{Stdout: c.Data}}
		if c.Stderr {
			ev = &hoexv1.RunEvent{Event: &hoexv1.RunEvent_Stderr{Stderr: c.Data}}
		}
		return stream.Send(ev)
	})
	if err != nil {
		ctxErr := stream.Context().Err()
		if ctxErr != nil {
			err = errCanceled
		}
		s.sink.Send(events.Finished{RunID: id, Time: time.Now(), Err: err})
		if ctxErr != nil {
			return status.FromContextError(ctxErr).Err()
		}
		return err
	}
	s.sink.Send(events.Finished{RunID: id, Time: time.Now(), ExitCode: code})
	return stream.Send(&hoexv1.RunEvent{Event: &hoexv1.RunEvent_ExitCode{ExitCode: int32(code)}})
}

// workDir maps the sandbox working directory to a directory inside the
// project. The sandbox mounts the project at the same path, so the path is
// used as is, but it must not leave the project root.
func (s *Server) workDir(cwd string) (string, error) {
	if cwd == "" {
		return s.root, nil
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("working directory must be absolute: %s", cwd)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("working directory does not exist on the host: %s", cwd)
	}
	rel, err := filepath.Rel(s.root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory is outside the project %s: %s", s.root, cwd)
	}
	return real, nil
}
