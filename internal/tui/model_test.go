package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/balesz/hoex/internal/events"
)

var t0 = time.Date(2026, 10, 16, 10, 48, 31, 0, time.Local)

type harness struct {
	t *testing.T
	m Model
}

func newHarness(t *testing.T, w, h int) *harness {
	m := New(NewFeed())
	m.now = func() time.Time { return t0.Add(3 * time.Second) }
	hs := &harness{t: t, m: m}
	hs.update(tea.WindowSizeMsg{Width: w, Height: h})
	return hs
}

func (h *harness) update(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	m, cmd := h.m.Update(msg)
	h.m = m.(Model)
	return cmd
}

func (h *harness) send(evs ...events.Event) tea.Cmd {
	h.t.Helper()
	return h.update(batchMsg(evs))
}

func (h *harness) key(k string) {
	h.t.Helper()
	var msg tea.KeyPressMsg
	switch k {
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	}
	h.update(msg)
}

// screen renders the view without styles and checks its geometry.
func (h *harness) screen() string {
	h.t.Helper()
	s := ansi.Strip(h.m.render())
	lines := strings.Split(s, "\n")
	if len(lines) != h.m.height {
		h.t.Errorf("screen has %d lines, want %d", len(lines), h.m.height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > h.m.width {
			h.t.Errorf("line %d is %d cells wide, window is %d: %q", i, w, h.m.width, l)
		}
	}
	return s
}

func (h *harness) contains(want ...string) {
	h.t.Helper()
	s := h.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			h.t.Errorf("screen does not contain %q:\n%s", w, s)
		}
	}
}

func (h *harness) selectedID() int {
	if r := h.m.selected(); r != nil {
		return r.id
	}
	return 0
}

func TestEmptyState(t *testing.T) {
	h := newHarness(t, 100, 20)
	h.contains("hoex host · starting…", "Waiting for commands from the sandbox")
	h.send(events.HostStarted{Time: t0, Root: "/home/u/app", Port: 47800, Rules: 2, Warnings: []string{"prefix bash allows \"bash -c\""}})
	h.contains("hoex host · /home/u/app · port 47800 · 2 rules · 0 runs", "⚠ prefix bash allows")
}

func TestPathsAreShortened(t *testing.T) {
	h := newHarness(t, 120, 20)
	h.m.homeDir = "/home/u"
	h.send(
		events.HostStarted{Time: t0, Root: "/home/u/app", Port: 47800, Warnings: []string{"/home/u/app/.hoex.yaml:3: example is not allowed"}},
		events.Started{RunID: 1, Time: t0, Argv: []string{"ls"}, Cwd: "/home/u/app/lib/src"},
	)
	h.contains("hoex host · ~/app ·", "⚠ .hoex.yaml:3: example", "in lib/src ·")
	h.send(events.Started{RunID: 2, Time: t0, Argv: []string{"ls"}, Cwd: "/home/u/app"})
	h.contains("in . ·")
}

func TestRunLifecycle(t *testing.T) {
	h := newHarness(t, 100, 20)
	h.send(events.HostStarted{Time: t0, Root: "/p", Port: 47800, Rules: 1})
	h.send(
		events.Started{RunID: 1, Time: t0, Argv: []string{"flutter", "test", "a b"}, Cwd: "/p"},
		events.Output{RunID: 1, Data: []byte("00:01 +1: loading\n00:02 +2: par")},
		events.Output{RunID: 1, Stderr: true, Data: []byte("warning: deprecated\n")},
	)
	h.contains(`● #1 10:48:31 flutter test "a b"`, "(1 running)", "running", "3s",
		"00:01 +1: loading", "00:02 +2: par", "warning: deprecated")

	h.send(events.Output{RunID: 1, Data: []byte("tial\n")}, events.Finished{RunID: 1, Time: t0.Add(1500 * time.Millisecond), ExitCode: 2})
	h.contains("✗ #1", "00:02 +2: partial", "1.5s", "exit 2")
	if strings.Contains(h.screen(), "running") {
		t.Error("still shown as running")
	}
}

func TestDeniedAndAborted(t *testing.T) {
	h := newHarness(t, 100, 20)
	h.send(events.Rejected{RunID: 1, Time: t0, Argv: []string{"rm", "-rf", "/"}, Reason: "command not allowed: rm -rf /"})
	h.contains("⊘ #1", "denied", "Rejected: command not allowed: rm -rf /")

	h.send(events.Started{RunID: 2, Time: t0, Argv: []string{"sleep", "30"}}, events.Finished{RunID: 2, Time: t0.Add(2 * time.Second), Err: errors.New("canceled by the sandbox")})
	h.contains("■ #2", "aborted: canceled by the sandbox")

	h.send(events.Rejected{RunID: 3, Time: t0, Reason: "invalid token"})
	h.contains("⊘ #3", "(no command)", "Rejected: invalid token")
}

func TestSelectionAndFollow(t *testing.T) {
	h := newHarness(t, 100, 20)
	for i := 1; i <= 3; i++ {
		h.send(events.Started{RunID: i, Time: t0, Argv: []string{"echo", "x"}})
	}
	if id := h.selectedID(); id != 3 {
		t.Fatalf("selected #%d, want the latest #3", id)
	}
	h.key("up")
	if id := h.selectedID(); id != 2 {
		t.Fatalf("after up: selected #%d, want #2", id)
	}
	h.send(events.Started{RunID: 4, Time: t0, Argv: []string{"echo", "x"}})
	if id := h.selectedID(); id != 2 {
		t.Errorf("a new run moved the selection away from #2 to #%d", id)
	}
	h.key("f")
	if id := h.selectedID(); id != 4 {
		t.Errorf("after f: selected #%d, want #4", id)
	}
	h.send(events.Started{RunID: 5, Time: t0, Argv: []string{"echo", "x"}})
	if id := h.selectedID(); id != 5 {
		t.Errorf("following: selected #%d, want #5", id)
	}
	h.key("up")
	h.key("down")
	h.send(events.Started{RunID: 6, Time: t0, Argv: []string{"echo", "x"}})
	if id := h.selectedID(); id != 6 {
		t.Errorf("moving back to the last run should resume following: selected #%d", id)
	}
}

func TestListScrollsToSelection(t *testing.T) {
	h := newHarness(t, 80, 30)
	for i := 1; i <= 30; i++ {
		h.send(events.Started{RunID: i, Time: t0, Argv: []string{"cmd" + string(rune('A'+i%26))}})
	}
	h.contains("#21 ", "#30")
	if strings.Contains(h.screen(), "#20 ") {
		t.Errorf("list shows more than %d runs", listRows)
	}
	for range 25 {
		h.key("up")
	}
	h.contains("#5 ")
	if strings.Contains(h.screen(), "#30") {
		t.Error("list did not scroll up with the selection")
	}
}

func TestOutputFollowsEndUnlessScrolledUp(t *testing.T) {
	h := newHarness(t, 80, 12)
	h.send(events.Started{RunID: 1, Time: t0, Argv: []string{"seq"}})
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line-" + strconv.Itoa(i) + "\n")
	}
	h.send(events.Output{RunID: 1, Data: []byte(b.String())})
	h.contains("line-100")

	h.update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	h.send(events.Output{RunID: 1, Data: []byte("line-101\n")})
	if strings.Contains(h.screen(), "line-101") {
		t.Error("output jumped to the end although the user scrolled up")
	}
	h.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	h.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	h.send(events.Output{RunID: 1, Data: []byte("line-102\n")})
	h.contains("line-102")
}

func TestLongLinesAndSmallWindow(t *testing.T) {
	h := newHarness(t, 60, 10)
	long := strings.Repeat("x", 300)
	h.send(
		events.HostStarted{Time: t0, Root: "/" + long, Port: 1, Warnings: []string{long, "b", "c", "d", "e"}},
		events.Started{RunID: 1, Time: t0, Argv: []string{"echo", long}, Cwd: "/" + long},
		events.Output{RunID: 1, Data: []byte(long + "\n")},
	)
	h.contains("+2 more warnings")
	h.update(tea.WindowSizeMsg{Width: 30, Height: 5})
	if got := h.m.render(); got != "hoex host: window too small" {
		t.Errorf("small window: %q", got)
	}
}

func TestHostStoppedQuits(t *testing.T) {
	h := newHarness(t, 80, 10)
	h.send(events.Stopping{Time: t0})
	h.contains("shutting down")
	cmd := h.send(events.HostStopped{})
	if cmd == nil {
		t.Fatal("no command after HostStopped")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("HostStopped did not quit the UI")
	}
}

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"\x1b[31mred\x1b[0m":     "red",
		"10%\r50%\r100%":         "100%",
		"windows line\r":         "windows line",
		"a\tb":                   "a    b",
		"bell\a and \x1b[2Kgone": "bell and gone",
	}
	for in, want := range tests {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOldLinesDropped(t *testing.T) {
	r := &run{}
	for range maxLines + 5 {
		r.write(false, []byte("x\n"))
	}
	if len(r.lines) != maxLines || r.dropped != 5 {
		t.Errorf("kept %d lines, dropped %d", len(r.lines), r.dropped)
	}
}

func TestFeedBatches(t *testing.T) {
	f := NewFeed()
	f.Send(events.Warning{Text: "a"})
	f.Send(events.Warning{Text: "b"})
	batch, ok := f.next()().(batchMsg)
	if !ok || len(batch) != 2 {
		t.Errorf("got %v, want one batch of 2 events", batch)
	}
}

func TestConfigChangeApproval(t *testing.T) {
	h := newHarness(t, 100, 20)
	h.send(events.HostStarted{Time: t0, Root: "/p", Port: 47800, Rules: 2, Warnings: []string{"old warning"}})
	h.send(events.Started{RunID: 1, Time: t0, Argv: []string{"echo", "x"}})

	var decided []bool
	h.send(events.ConfigChanged{ID: 1, Time: t0, File: "/p/.hoex.yaml", Diff: []string{"+ prefix rm", "- prefix flutter", "~ port 1 → 2"},
		Decide: func(ok bool) { decided = append(decided, ok) }})
	h.contains("⚠ .hoex.yaml changed", "not active until you approve", "+ prefix rm", "- prefix flutter", "~ port 1 → 2",
		"a approve config change · r reject")
	if v := h.m.View(); !strings.Contains(v.WindowTitle, "waiting for approval") {
		t.Errorf("window title %q", v.WindowTitle)
	}

	h.key("a")
	h.key("a")
	if len(decided) != 1 || !decided[0] {
		t.Fatalf("decisions %v, want one approval", decided)
	}
	h.contains("#1  echo x") // back to the run
	h.send(events.ConfigResolved{ID: 1, Time: t0, Outcome: events.ConfigApproved, Rules: 3, Warnings: []string{"new warning"}})
	h.contains("3 rules", "⚠ new warning")
	if strings.Contains(h.screen(), "old warning") {
		t.Error("warning of the old config still shown")
	}
}

func TestConfigChangeRejectAndWithdraw(t *testing.T) {
	h := newHarness(t, 100, 20)
	var decided []bool
	decide := func(ok bool) { decided = append(decided, ok) }
	h.send(events.ConfigChanged{ID: 1, Time: t0, File: "/p/.hoex.yaml", Diff: []string{"+ prefix rm"}, Decide: decide})
	h.key("r")
	if len(decided) != 1 || decided[0] {
		t.Fatalf("decisions %v, want one rejection", decided)
	}

	h.send(events.ConfigChanged{ID: 2, Time: t0, File: "/p/.hoex.yaml", Diff: []string{"+ prefix mv"}, Decide: decide})
	h.contains("+ prefix mv")
	h.send(events.ConfigResolved{ID: 2, Time: t0, Outcome: events.ConfigWithdrawn})
	if strings.Contains(h.screen(), "prefix mv") {
		t.Error("withdrawn change still shown")
	}
	h.key("a")
	if len(decided) != 1 {
		t.Error("a key decided a withdrawn change")
	}
}

func TestLongConfigDiffIsCut(t *testing.T) {
	h := newHarness(t, 80, 12)
	var diff []string
	for i := range 30 {
		diff = append(diff, "+ prefix cmd"+strconv.Itoa(i))
	}
	h.send(events.ConfigChanged{ID: 1, Time: t0, File: "/p/.hoex.yaml", Diff: diff, Decide: func(bool) {}})
	h.contains("+ prefix cmd0", "more", "a approve · r reject")
}

func TestLatestWarningShown(t *testing.T) {
	h := newHarness(t, 100, 20)
	h.send(events.Warning{Time: t0, Text: "first"}, events.Warning{Time: t0, Text: "second"})
	h.contains("10:48:31 ⚠ second")
	if strings.Contains(h.screen(), "first") {
		t.Error("older warning still shown")
	}
}

func TestListRowScrollsSideways(t *testing.T) {
	h := newHarness(t, 60, 20)
	long := "echo " + strings.Repeat("a", 80) + "END"
	h.send(events.Started{RunID: 1, Time: t0, Argv: []string{long}}, events.Started{RunID: 2, Time: t0, Argv: []string{long}})
	if strings.Contains(h.screen(), "END") {
		t.Fatal("long command not truncated")
	}
	for range 10 {
		h.key("l")
	}
	if n := strings.Count(h.screen(), "END"); n != 1 {
		t.Errorf("after l: END shown %d times, want once on the selected row", n)
	}
	h.key("k")
	if h.m.listX != 0 {
		t.Errorf("new selection kept the scroll offset %d", h.m.listX)
	}
	h.key("l")
	h.key("h")
	if h.m.listX != 0 {
		t.Errorf("after l and h: offset %d", h.m.listX)
	}
}

func TestOutputPaneKeys(t *testing.T) {
	h := newHarness(t, 60, 20)
	h.send(events.Started{RunID: 1, Time: t0, Argv: []string{"seq"}}, events.Started{RunID: 2, Time: t0, Argv: []string{"seq"}})
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		b.WriteString("line-" + strconv.Itoa(i) + " " + strings.Repeat("x", 80) + "END" + strconv.Itoa(i) + "\n")
	}
	h.send(events.Output{RunID: 2, Data: []byte(b.String())})
	h.contains("line-50")

	h.key("J")
	if h.m.focus != outputPane {
		t.Fatal("J did not focus the output")
	}
	h.contains("K run list")
	h.key("k")
	if id := h.selectedID(); id != 2 {
		t.Errorf("k in the output changed the selection to #%d", id)
	}
	if strings.Contains(h.screen(), "line-50 ") {
		t.Error("k did not scroll the output up")
	}
	for range 10 {
		h.key("l")
	}
	h.contains("END49")
	h.key("j")
	h.contains("END50")

	h.key("K")
	h.key("k")
	if id := h.selectedID(); id != 1 {
		t.Errorf("k in the list after K: selected #%d, want #1", id)
	}
}
