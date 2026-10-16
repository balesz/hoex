package events

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestLoggerInterleavedRuns(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf)
	at := time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local)

	l.Send(Started{RunID: 1, Time: at, Argv: []string{"flutter", "test", "a b"}, Cwd: "/p"})
	l.Send(Started{RunID: 2, Time: at, Argv: []string{"dart", "format", "."}, Cwd: "/p"})
	l.Send(Output{RunID: 1, Data: []byte("par")})
	l.Send(Output{RunID: 2, Data: []byte("formatted\n")})
	l.Send(Output{RunID: 1, Data: []byte("tial\nsecond\nno newline")})
	l.Send(Output{RunID: 1, Stderr: true, Data: []byte("warn\r\n")})
	l.Send(Finished{RunID: 1, Time: at, ExitCode: 1})
	l.Send(Finished{RunID: 2, Time: at, Err: errors.New("canceled")})
	l.Send(Rejected{RunID: 3, Time: at, Argv: []string{"rm", "-rf", "/"}, Reason: "command not allowed"})

	want := `09:30:00 #1 run   flutter test "a b"  (in /p)
09:30:00 #2 run   dart format .  (in /p)
#2 │ formatted
#1 │ partial
#1 │ second
#1 ┃ warn
#1 │ no newline
09:30:00 #1 exit  1
09:30:00 #2 abort canceled
09:30:00 #3 deny  rm -rf /  (command not allowed)
`
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
