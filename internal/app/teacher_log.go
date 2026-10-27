package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
)

// A teacherLog writes down what goes between the app and the teacher, a
// session at a time, to teacher.log in the cache: to find out what went
// wrong when the teacher goes quiet, or says the same again. The sound
// either way is left out; what was said, written down, is in.
type teacherLog struct {
	f     *os.File
	start time.Time
}

// openTeacherLog starts the log of a session over, or returns nil.
func openTeacherLog() *teacherLog {
	dir, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		return nil
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return nil
	}
	f, err := os.Create(filepath.Join(dir, "teacher.log"))
	if err != nil {
		return nil
	}
	l := &teacherLog{f: f, start: time.Now()}
	l.printf("session %s", l.start.Format(time.DateTime))
	return l
}

// printf writes a line, after the seconds since the session started.
func (l *teacherLog) printf(format string, args ...any) {
	if l == nil {
		return
	}
	fmt.Fprintf(l.f, "%7.1f  %s\n", time.Since(l.start).Seconds(), fmt.Sprintf(format, args...))
}

func (l *teacherLog) close() {
	if l != nil {
		l.f.Close()
	}
}

// A loggedSession logs what the app sends the teacher, but for the
// microphone's sound.
type loggedSession struct {
	teacherSession
	log *teacherLog
}

func (s loggedSession) Instruct(instructions string) error {
	s.log.printf("→ instructions (%d characters)", len([]rune(instructions)))
	return s.teacherSession.Instruct(instructions)
}

func (s loggedSession) Tell(text string) error {
	s.log.printf("→ tell %s", text)
	return s.teacherSession.Tell(text)
}

func (s loggedSession) Answer(id string, result any) error {
	out, _ := json.Marshal(result)
	s.log.printf("→ answer %s %s", id, out)
	return s.teacherSession.Answer(id, result)
}

func (s loggedSession) Respond() error {
	s.log.printf("→ respond")
	return s.teacherSession.Respond()
}

func (s loggedSession) Cancel() error {
	s.log.printf("→ cancel")
	return s.teacherSession.Cancel()
}

func (s loggedSession) Close() {
	s.log.printf("→ close")
	s.teacherSession.Close()
}
