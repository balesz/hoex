package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balesz/hoex/internal/events"
)

func readRecords(t *testing.T, dir string) []Record {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var recs []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestLog(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Send(events.HostStarted{Time: t0, Port: 47800, Rules: 2})
	l.Send(events.Started{RunID: 1, Time: t0, Argv: []string{"flutter", "test"}, Cwd: "/p"})
	l.Send(events.Output{RunID: 1, Data: []byte("secret output")})
	l.Send(events.Finished{RunID: 1, Time: t0.Add(1500 * time.Millisecond), ExitCode: 1})
	l.Send(events.Rejected{RunID: 2, Time: t0, Argv: []string{"rm", "-rf", "/"}, Cwd: "/p", Reason: "command not allowed"})
	l.Send(events.Started{RunID: 3, Time: t0, Argv: []string{"sleep", "30"}, Cwd: "/p"})
	l.Send(events.Finished{RunID: 3, Time: t0.Add(2 * time.Second), Err: errors.New("context canceled")})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening appends.
	l, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Send(events.HostStarted{Time: t0, Port: 47800, Rules: 2})
	_ = l.Close()

	recs := readRecords(t, dir)
	if got, want := len(recs), 7; got != want {
		t.Fatalf("got %d records %+v, want %d", got, recs, want)
	}
	run, deny, aborted := recs[1], recs[2], recs[3]
	if run.Event != "run" || run.ExitCode == nil || *run.ExitCode != 1 || run.DurationMS != 1500 || run.Argv[0] != "flutter" {
		t.Errorf("run record: %+v", run)
	}
	if deny.Event != "deny" || deny.Reason != "command not allowed" || deny.ExitCode != nil {
		t.Errorf("deny record: %+v", deny)
	}
	if aborted.Aborted != "context canceled" || aborted.ExitCode != nil {
		t.Errorf("aborted record: %+v", aborted)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if json.Valid(data) || len(data) == 0 {
		t.Error("expected JSON lines, not one JSON document")
	}
	for _, r := range recs {
		if r.Event == "run" && r.Reason != "" {
			t.Errorf("unexpected reason on run: %+v", r)
		}
	}
	if bytes.Contains(data, []byte("secret output")) {
		t.Error("command output leaked into the audit log")
	}
}

func TestExitCodeZeroIsRecorded(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Send(events.Started{RunID: 1, Time: time.Now(), Argv: []string{"true"}})
	l.Send(events.Finished{RunID: 1, Time: time.Now(), ExitCode: 0})
	_ = l.Close()
	if r := readRecords(t, dir)[0]; r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("exit code 0 missing: %+v", r)
	}
}
