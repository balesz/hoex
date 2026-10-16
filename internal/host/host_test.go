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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/balesz/hoex/internal/approval"
	"github.com/balesz/hoex/internal/client"
	"github.com/balesz/hoex/internal/connfile"
	"github.com/balesz/hoex/internal/events"
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
	root   string
	log    *syncBuffer
	events *recorder
	// stop stops the host and waits for it.
	stop func()
}

// recorder keeps the config events the host sends.
type recorder struct {
	mu      sync.Mutex
	changed []events.ConfigChanged
	outcome map[int]string
}

func (r *recorder) Send(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := e.(type) {
	case events.ConfigChanged:
		r.changed = append(r.changed, e)
	case events.ConfigResolved:
		r.outcome[e.ID] = e.Outcome
	}
}

func (r *recorder) changes() []events.ConfigChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.ConfigChanged(nil), r.changed...)
}

func (r *recorder) outcomeOf(id int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outcome[id]
}

// start runs a host daemon for a fresh project and stops it at test end.
func start(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".hoex.yaml"), projectConfig, 0o644)
	return startIn(t, root, nil)
}

// startIn runs a host daemon in root with the given approval store.
func startIn(t *testing.T, root string, approvals *approval.Store) *fixture {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{root: root, log: &syncBuffer{}, events: &recorder{outcome: make(map[int]string)}}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- host.Run(ctx, host.Options{
			Root:     root,
			Listener: lis,
			Display:  events.Multi{events.NewLogger(f.log), f.events},
			Ready:    func(net.Addr) { close(ready) },
			// File events are not delivered everywhere; make sure the
			// interval check alone is fast enough for the tests.
			WatchInterval: 50 * time.Millisecond,
			Approvals:     approvals,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("host.Run: %v", err)
	}
	f.stop = sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("host.Run: %v", err)
		}
		if _, err := os.Stat(connfile.Path(root)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("connection file not removed: %v", err)
		}
	})
	t.Cleanup(func() {
		f.stop()
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
		Root: t.TempDir(), Port: lis.Addr().(*net.TCPAddr).Port, Display: events.NewLogger(&syncBuffer{}),
	})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("err = %v", err)
	}
}

func TestInvalidConfigStopsStartup(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".hoex.yaml"), "allow:\n  - prefix: echo\n", 0o644)
	err := host.Run(context.Background(), host.Options{Root: root, Display: events.NewLogger(&syncBuffer{})})
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

func TestClientCancelStopsHostCommand(t *testing.T) {
	f := start(t)
	write(t, filepath.Join(f.root, "sleeper.sh"), "sleep 1000 & echo $! > pid; wait\n", 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		done <- client.Run(ctx, client.Options{
			Argv: []string{"sh", "sleeper.sh"}, Cwd: f.root, Host: "127.0.0.1", Stdout: &out, Stderr: &out,
		})
	}()
	pid := waitPID(t, filepath.Join(f.root, "pid"))
	cancel()
	<-done

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && !zombie(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("host command still running after the sandbox side canceled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitLog(t, f, "abort canceled by the sandbox")
}

func TestParallelRunsDoNotMix(t *testing.T) {
	f := start(t)
	write(t, filepath.Join(f.root, "lines.sh"), "i=0; while [ $i -lt 2000 ]; do echo \"$1$1$1\"; i=$((i+1)); done\n", 0o644)

	var wg sync.WaitGroup
	results := make([]result, 3)
	for i, tag := range []string{"A", "B", "C"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.run(t, f.root, "sh", "lines.sh", tag)
		}()
	}
	wg.Wait()
	for i, tag := range []string{"A", "B", "C"} {
		want := strings.Repeat(tag+tag+tag+"\n", 2000)
		if results[i].code != 0 || results[i].stdout != want {
			t.Errorf("run %s: code=%d, %d bytes of stdout, want %d", tag, results[i].code, len(results[i].stdout), len(want))
		}
	}
	// In the host log every output line carries the ID of its own run.
	runTag := map[string]string{}
	for _, line := range strings.Split(f.log.String(), "\n") {
		if fields := strings.Fields(line); len(fields) == 8 && fields[2] == "run" {
			runTag[fields[1]] = fields[5]
		}
	}
	if len(runTag) != 3 {
		t.Fatalf("found %d runs in the host log, want 3", len(runTag))
	}
	for _, line := range strings.Split(f.log.String(), "\n") {
		id, rest, ok := strings.Cut(line, " │ ")
		if !ok {
			continue
		}
		if tag := runTag[id]; rest != tag+tag+tag {
			t.Fatalf("host log line %q does not belong to run %s (%s)", line, id, tag)
		}
	}
}

func TestAuditLog(t *testing.T) {
	f := start(t)
	f.run(t, f.root, "echo", "secret-output")
	f.run(t, f.root, "touch", "x")
	logFile := filepath.Join(f.root, connfile.Dir, "hoex.log")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(logFile)
		return bytes.Contains(data, []byte(`"event":"deny"`))
	})
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"start"`, `"event":"run"`, `"argv":["echo","secret-output"]`, `"exit_code":0`, `"argv":["touch","x"]`, `"reason":"command not allowed: touch x"`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("audit log does not contain %s:\n%s", want, data)
		}
	}
	if bytes.Contains(data, []byte("secret-output\\n")) {
		t.Error("command output in the audit log")
	}
}

const touchRule = `  - prefix: touch
    description: touch
    example: touch file
`

// waitChange waits for the n-th config change (1-based).
func (f *fixture) waitChange(t *testing.T, n int) events.ConfigChanged {
	t.Helper()
	waitFor(t, func() bool { return len(f.events.changes()) >= n })
	return f.events.changes()[n-1]
}

func (f *fixture) waitOutcome(t *testing.T, id int, want string) {
	t.Helper()
	waitFor(t, func() bool { return f.events.outcomeOf(id) != "" })
	if got := f.events.outcomeOf(id); got != want {
		t.Fatalf("change %d: outcome %q, want %q", id, got, want)
	}
}

func TestConfigChangeNeedsApproval(t *testing.T) {
	f := start(t)
	config := filepath.Join(f.root, ".hoex.yaml")
	write(t, config, projectConfig+touchRule, 0o644)

	c := f.waitChange(t, 1)
	if strings.Join(c.Diff, "\n") != "+ prefix touch" {
		t.Errorf("diff = %q", c.Diff)
	}
	if got := f.run(t, f.root, "touch", "marker"); got.code != exitcode.Denied {
		t.Fatalf("before approval: code %d, stderr %q", got.code, got.stderr)
	}

	c.Decide(true)
	f.waitOutcome(t, c.ID, events.ConfigApproved)
	if got := f.run(t, f.root, "touch", "marker"); got.code != 0 {
		t.Fatalf("after approval: code %d, stderr %q", got.code, got.stderr)
	}

	data, err := os.ReadFile(filepath.Join(f.root, connfile.Dir, "hoex.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"config_change","change":1`, `"diff":["+ prefix touch"]`, `"event":"config_approved","rules":7,"change":1`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("audit log does not contain %s:\n%s", want, data)
		}
	}
}

func TestConfigChangeRejected(t *testing.T) {
	f := start(t)
	config := filepath.Join(f.root, ".hoex.yaml")
	write(t, config, projectConfig+touchRule, 0o644)
	c := f.waitChange(t, 1)
	c.Decide(false)
	f.waitOutcome(t, c.ID, events.ConfigRejected)
	c.Decide(true) // too late, ignored

	if got := f.run(t, f.root, "touch", "marker"); got.code != exitcode.Denied {
		t.Fatalf("rejected rule applied: code %d", got.code)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(f.events.changes()); n != 1 {
		t.Fatalf("the rejected content was offered %d times", n)
	}

	// Another change is offered again.
	write(t, config, strings.Replace(projectConfig+touchRule, "prefix: touch", "prefix: rm", 1), 0o644)
	if c2 := f.waitChange(t, 2); strings.Join(c2.Diff, " ") != "+ prefix rm" {
		t.Errorf("second diff = %q", c2.Diff)
	}
}

func TestConfigChangeWithdrawn(t *testing.T) {
	f := start(t)
	config := filepath.Join(f.root, ".hoex.yaml")
	write(t, config, projectConfig+touchRule, 0o644)
	c := f.waitChange(t, 1)
	write(t, config, projectConfig, 0o644)
	f.waitOutcome(t, c.ID, events.ConfigWithdrawn)
	c.Decide(true) // withdrawn, ignored
	if got := f.run(t, f.root, "touch", "marker"); got.code != exitcode.Denied {
		t.Fatalf("withdrawn change applied: code %d", got.code)
	}
}

func TestInvalidConfigChangeKeepsRules(t *testing.T) {
	f := start(t)
	write(t, filepath.Join(f.root, ".hoex.yaml"), "allow:\n  - prefix: touch\n", 0o644)
	waitLog(t, f, ".hoex.yaml changed but is invalid, the active rules stay in effect")
	if got := f.run(t, f.root, "echo", "ok"); got.code != 0 || got.stdout != "ok\n" {
		t.Errorf("active rules lost: %+v", got)
	}
	if n := len(f.events.changes()); n != 0 {
		t.Errorf("invalid config offered for approval")
	}
}

func TestCommentOnlyChangeNeedsNoApproval(t *testing.T) {
	f := start(t)
	write(t, filepath.Join(f.root, ".hoex.yaml"), "# comment\n"+projectConfig, 0o644)
	time.Sleep(300 * time.Millisecond)
	if n := len(f.events.changes()); n != 0 {
		t.Errorf("a comment change was offered for approval")
	}
}

func TestChangeWhileStoppedNeedsApproval(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	approvals := &approval.Store{Dir: t.TempDir()}
	config := filepath.Join(root, ".hoex.yaml")
	write(t, config, projectConfig, 0o644)

	// First start: the file counts as approved.
	f := startIn(t, root, approvals)
	if got := f.run(t, root, "echo", "ok"); got.code != 0 {
		t.Fatalf("first start: %+v", got)
	}
	f.stop()

	// Changed while the host was not running.
	write(t, config, projectConfig+touchRule, 0o644)
	f = startIn(t, root, approvals)
	c := f.waitChange(t, 1)
	if strings.Join(c.Diff, " ") != "+ prefix touch" {
		t.Errorf("diff = %q", c.Diff)
	}
	if got := f.run(t, root, "touch", "marker"); got.code != exitcode.Denied {
		t.Fatalf("unapproved rule applied at startup: code %d", got.code)
	}
	if got := f.run(t, root, "echo", "ok"); got.code != 0 {
		t.Fatalf("approved rules lost: %+v", got)
	}
	c.Decide(true)
	f.waitOutcome(t, c.ID, events.ConfigApproved)
	f.stop()

	// The approval is remembered.
	f = startIn(t, root, approvals)
	if got := f.run(t, root, "touch", "marker"); got.code != 0 {
		t.Fatalf("approved rule after restart: code %d, %s", got.code, got.stderr)
	}
	if n := len(f.events.changes()); n != 0 {
		t.Errorf("approved config offered again")
	}
}

func TestRejectedAtStartupStaysRejected(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	approvals := &approval.Store{Dir: t.TempDir()}
	if err := approvals.Save(root, []byte(projectConfig)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".hoex.yaml"), projectConfig+touchRule, 0o644)

	f := startIn(t, root, approvals)
	c := f.waitChange(t, 1)
	c.Decide(false)
	f.waitOutcome(t, c.ID, events.ConfigRejected)
	f.stop()

	f = startIn(t, root, approvals)
	f.waitChange(t, 1) // asked again after a restart
	if got := f.run(t, root, "touch", "marker"); got.code != exitcode.Denied {
		t.Fatalf("rejected rule applied: code %d", got.code)
	}
}

func waitPID(t *testing.T, file string) int {
	t.Helper()
	var pid int
	waitFor(t, func() bool {
		data, err := os.ReadFile(file)
		if err != nil || !bytes.HasSuffix(data, []byte("\n")) {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid > 0
	})
	return pid
}

func zombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] == "Z"
}

func waitLog(t *testing.T, f *fixture, s string) {
	t.Helper()
	waitFor(t, func() bool { return strings.Contains(f.log.String(), s) })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInstalledSkillFollowsRules(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".hoex.yaml"), projectConfig, 0o644)
	skillFile := filepath.Join(root, ".claude", "skills", "hoex", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, skillFile, "outdated", 0o644)

	f := startIn(t, root, nil)
	skill := func() string {
		data, _ := os.ReadFile(skillFile)
		return string(data)
	}
	if !strings.Contains(skill(), "### `echo`") {
		t.Fatalf("skill not regenerated at startup:\n%s", skill())
	}

	write(t, filepath.Join(root, ".hoex.yaml"), projectConfig+touchRule, 0o644)
	c := f.waitChange(t, 1)
	if strings.Contains(skill(), "### `touch`") {
		t.Fatal("skill lists a rule before approval")
	}
	c.Decide(true)
	f.waitOutcome(t, c.ID, events.ConfigApproved)
	if !strings.Contains(skill(), "### `touch`") {
		t.Error("skill not updated after approval")
	}
}

func TestLinksToOutsideAreNotWritten(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, ".bashrc")
	write(t, victim, "original\n", 0o644)

	for name, link := range map[string]func(root string) error{
		"log file": func(root string) error {
			if err := os.Mkdir(filepath.Join(root, ".hoex"), 0o755); err != nil {
				return err
			}
			return os.Symlink(victim, filepath.Join(root, ".hoex", "hoex.log"))
		},
		"state directory": func(root string) error {
			return os.Symlink(outside, filepath.Join(root, ".hoex"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, ".hoex.yaml"), projectConfig, 0o644)
			if err := link(root); err != nil {
				t.Fatal(err)
			}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			err = host.Run(context.Background(), host.Options{Root: root, Listener: lis, Display: events.NewLogger(&syncBuffer{})})
			if err == nil {
				t.Fatal("host started with a link to outside the project")
			}
			if data, _ := os.ReadFile(victim); string(data) != "original\n" {
				t.Errorf("file outside the project changed: %q", data)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 1 {
				t.Errorf("files created outside the project: %v", entries)
			}
		})
	}
}
