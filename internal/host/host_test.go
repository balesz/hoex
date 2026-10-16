package host_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balesz/hoex/internal/client"
	"github.com/balesz/hoex/internal/connfile"
	"github.com/balesz/hoex/internal/exitcode"
	"github.com/balesz/hoex/internal/host"
)

const projectConfig = `allow:
  - prefix: echo
    description: echo
    example: echo hi
  - prefix: cat
    description: cat
    example: cat file
  - prefix: pwd
    description: pwd
    example: pwd
  - prefix: sh
    description: test helper
    example: sh err.sh
  - prefix: hoex-no-such-command
    description: missing
    example: hoex-no-such-command
  - prefix: ./script.sh
    description: script
    example: ./script.sh
`

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fixture struct {
	root string
	log  *syncBuffer
}

// start runs a host daemon for a fresh project and stops it at test end.
func start(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".hoex.yaml"), projectConfig, 0o644)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{root: root, log: &syncBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- host.Run(ctx, host.Options{
			Root:     root,
			Listener: lis,
			Out:      f.log,
			Ready:    func(net.Addr) { close(ready) },
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("host.Run: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("host.Run: %v", err)
		}
		if _, err := os.Stat(connfile.Path(root)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("connection file not removed: %v", err)
		}
		if t.Failed() {
			t.Logf("host log:\n%s", f.log)
		}
	})
	return f
}

type result struct {
	stdout, stderr string
	code           int
}

func (f *fixture) run(t *testing.T, cwd string, argv ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := client.Run(context.Background(), client.Options{
		Argv: argv, Cwd: cwd, Host: "127.0.0.1", Stdout: &stdout, Stderr: &stderr,
	})
	return result{stdout.String(), stderr.String(), code}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	f := start(t)
	sub := filepath.Join(f.root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.root, "script.sh"), "#!/bin/sh\necho script ran in \"$PWD\"\n", 0o755)
	write(t, filepath.Join(f.root, "err.sh"), "echo out; echo err >&2; exit 3\n", 0o644)

	tests := []struct {
		name   string
		cwd    string
		argv   []string
		stdout string
		stderr string // substring
		code   int
	}{
		{"stdout", f.root, []string{"echo", "hello", "world"}, "hello world\n", "", 0},
		{"arguments are not split or expanded", f.root, []string{"echo", "a  b", "$HOME", "*"}, "a  b $HOME *\n", "", 0},
		{"stderr and exit code", f.root, []string{"sh", "err.sh"}, "out\n", "err\n", 3},
		{"killed by signal", f.root, []string{"sh", "-c", "kill -TERM $$"}, "", "", 143},
		{"subdirectory as cwd", sub, []string{"pwd"}, sub + "\n", "", 0},
		{"shebang script", f.root, []string{"./script.sh"}, "script ran in " + f.root + "\n", "", 0},
		{"command not found", f.root, []string{"hoex-no-such-command"}, "", "hoex-no-such-command", 127},
		{"not allowed", f.root, []string{"touch", "marker"}, "", "rejected by the host: command not allowed: touch marker", exitcode.Denied},
		{"chaining", f.root, []string{"echo", "a;", "touch", "marker"}, "", "chaining is not allowed", exitcode.Denied},
		{"cwd outside project", "/", []string{"pwd"}, "", "no .hoex/connection found", exitcode.Unavailable},
		{"empty command", f.root, nil, "", "no command given", exitcode.Usage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.run(t, tt.cwd, tt.argv...)
			if got.stdout != tt.stdout || !strings.Contains(got.stderr, tt.stderr) || got.code != tt.code {
				t.Errorf("got stdout=%q stderr=%q code=%d\nwant stdout=%q stderr~%q code=%d",
					got.stdout, got.stderr, got.code, tt.stdout, tt.stderr, tt.code)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(f.root, "marker")); err == nil {
		t.Error("a rejected command ran: marker file exists")
	}
	for _, want := range []string{"run   echo hello world", "exit  3", "deny  touch marker  (command not allowed"} {
		if !strings.Contains(f.log.String(), want) {
			t.Errorf("host log does not contain %q", want)
		}
	}
}

func TestBinaryOutputIsByteExact(t *testing.T) {
	f := start(t)
	data := make([]byte, 5<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.root, "blob"), string(data), 0o644)

	got := f.run(t, f.root, "cat", "blob")
	if got.code != 0 || got.stdout != string(data) {
		t.Fatalf("code=%d, got %d bytes, want %d identical bytes", got.code, len(got.stdout), len(data))
	}
}

func TestCwdOutsideProjectRejected(t *testing.T) {
	f := start(t)
	// A directory outside the project that still sees the project's
	// connection file: a symlink from inside the project to the outside.
	outside := t.TempDir()
	link := filepath.Join(f.root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	got := f.run(t, link, "pwd")
	if got.code != exitcode.Denied || !strings.Contains(got.stderr, "outside the project") {
		t.Errorf("got %+v", got)
	}
}

func TestBadToken(t *testing.T) {
	f := start(t)
	info := readConn(t, f.root)
	info.Token = "wrong"
	if err := connfile.Write(f.root, info); err != nil {
		t.Fatal(err)
	}
	got := f.run(t, f.root, "echo", "x")
	if got.code != exitcode.Unavailable || !strings.Contains(got.stderr, "rejected the token") {
		t.Errorf("got %+v", got)
	}
	if !strings.Contains(f.log.String(), "invalid token") {
		t.Error("host log does not show the rejected token")
	}
}

func TestHostNotRunning(t *testing.T) {
	root := t.TempDir()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	if err := connfile.Write(root, connfile.Info{Port: port, Token: "t"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := client.Run(ctx, client.Options{Argv: []string{"echo"}, Cwd: root, Host: "127.0.0.1", Stdout: &stdout, Stderr: &stderr})
	if code != exitcode.Unavailable || !strings.Contains(stderr.String(), "sbx policy allow network localhost:") {
		t.Errorf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestPortInUse(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	err = host.Run(context.Background(), host.Options{
		Root: t.TempDir(), Port: lis.Addr().(*net.TCPAddr).Port, Out: &syncBuffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("err = %v", err)
	}
}

func TestInvalidConfigStopsStartup(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".hoex.yaml"), "allow:\n  - prefix: echo\n", 0o644)
	err := host.Run(context.Background(), host.Options{Root: root, Out: &syncBuffer{}})
	if err == nil || !strings.Contains(err.Error(), ".hoex.yaml:2: rule is missing description") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(connfile.Path(root)); err == nil {
		t.Error("connection file written despite invalid config")
	}
}

func readConn(t *testing.T, root string) connfile.Info {
	t.Helper()
	_, info, err := connfile.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
