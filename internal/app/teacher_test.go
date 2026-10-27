package app

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/teacher"
)

// fakeTeacher stands in for the model: it keeps what the app tells it.
type fakeTeacher struct {
	heard        int
	instructions []string
	answers      map[string]any
	responded    int
	closed       bool
	told         []string
}

func (f *fakeTeacher) Tell(s string) error { f.told = append(f.told, s); return nil }

func (f *fakeTeacher) Hear(s []float32) error { f.heard += len(s); return nil }
func (f *fakeTeacher) Instruct(s string) error {
	f.instructions = append(f.instructions, s)
	return nil
}
func (f *fakeTeacher) Respond() error { f.responded++; return nil }
func (f *fakeTeacher) Close()         { f.closed = true }
func (f *fakeTeacher) Answer(id string, r any) error {
	f.answers[id] = r
	return nil
}

// teacherApp is takeApp with the teacher on and connected, the model
// fake, and what the microphone gives the teacher in hear.
func teacherApp(t *testing.T) (*app, *ui.Tester, *fakeMic, *fakeTeacher, func([]float32)) {
	t.Helper()
	a, tt, m := takeApp(t)
	f := &fakeTeacher{answers: map[string]any{}}
	var hear func([]float32)
	oldKey, oldDial, oldMic := teacherKey, dialTeacher, teacherMic
	teacherKey = func() string { return "test-key" }
	dialed := make(chan teacher.Config, 1)
	dialTeacher = func(cfg teacher.Config, got func(teacher.Event)) (teacherSession, error) {
		dialed <- cfg
		return f, nil
	}
	teacherMic = func(got func([]float32)) (interface{ Stop() }, error) {
		hear = got
		return &fakeMic{}, nil
	}
	t.Cleanup(func() { teacherKey, dialTeacher, teacherMic = oldKey, oldDial, oldMic })
	t.Cleanup(a.stopTeacher)
	if err := tt.Click("AI 老师"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("did not connect")
	}
	// The window would post this, connected.
	a.teacherDialed(a.teacher.gen, f, nil)
	tt.Frame()
	// Connected, the teacher is asked to greet the listener and start on
	// the line the lesson is at; it does, and is done.
	if len(f.told) != 1 || !strings.Contains(f.told[0], "第 2 句") || f.responded != 1 {
		t.Fatalf("told %q, responded %d", f.told, f.responded)
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	f.responded = 0
	return a, tt, m, f, hear
}

// The teacher, turned on with no API key, asks for one, saying nothing
// of connecting, which it used to; pasted, the key is kept and the
// teacher connects with it, told the song's lines and the tools. Turned
// off, it hangs up.
func TestTeacherToggle(t *testing.T) {
	a, tt, _ := takeApp(t)
	oldKey, oldSave, oldDial := teacherKey, saveTeacherKey, dialTeacher
	defer func() { teacherKey, saveTeacherKey, dialTeacher = oldKey, oldSave, oldDial }()
	teacherKey = func() string { return "" }
	var saved string
	saveTeacherKey = func(key string) error { saved = key; return nil }
	dialed := make(chan teacher.Config, 1)
	dialTeacher = func(cfg teacher.Config, got func(teacher.Event)) (teacherSession, error) {
		dialed <- cfg
		return &fakeTeacher{}, nil
	}
	tt.Click("AI 老师")
	tt.Frame()
	if a.teacher.on || !a.teacher.ask {
		t.Fatalf("with no key: on %v, asking %v", a.teacher.on, a.teacher.ask)
	}
	if strings.Contains(strings.Join(tt.Texts(), " "), "正在连接") {
		t.Error("says it connects, asking for the key")
	}
	if err := tt.Click("DashScope API Key"); err != nil {
		t.Fatal(err)
	}
	tt.Type(" test-key ")
	tt.Frame()
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cfg := <-dialed
	if saved != "test-key" || a.teacher.key != "" || a.teacher.ask {
		t.Errorf("kept %q, left %q in the field, asking %v", saved, a.teacher.key, a.teacher.ask)
	}
	if cfg.Key != "test-key" || !strings.Contains(cfg.Instructions, "2. 世界で僕は独りぼっちだった") {
		t.Errorf("connected with key %q, told %q", cfg.Key, cfg.Instructions)
	}
	var names []string
	for _, tool := range cfg.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "play_line") || !strings.Contains(got, "start_recording") {
		t.Errorf("tools %s", got)
	}
	f := &fakeTeacher{}
	a.teacherDialed(a.teacher.gen, f, nil)
	tt.Frame()
	tt.Click("AI 老师")
	tt.Frame()
	if a.teacher.on || !f.closed {
		t.Errorf("turned off: on %v, hung up %v", a.teacher.on, f.closed)
	}
}

// A line the teacher plays plays once, with no pass to sing along, and
// the teacher hears what it came to only once it has played: until
// then, and while the teacher answers, the microphone does not go to it.
func TestTeacherPlaysLine(t *testing.T) {
	a, _, _, f, hear := teacherApp(t)
	a.teacherTick()
	hear(make([]float32, 4800))
	if f.heard != 1600 {
		t.Fatalf("heard %d samples of 100 ms, want 1600 at 16 kHz", f.heard)
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "我们来听第一句"})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if n, alone := a.pl.Line(); n != 0 || !alone || !a.pl.Playing() {
		t.Fatalf("line %d, alone %v, playing %v", n, alone, a.pl.Playing())
	}
	hear(make([]float32, 4800))
	if len(f.answers) != 0 || f.heard != 1600 {
		t.Errorf("as the line plays: answered %v, heard %d", f.answers, f.heard)
	}

	// The line plays to its end, and pauses.
	a.pl.StopLine()
	a.teacherTick()
	r, _ := f.answers["c1"].(map[string]any)
	if r["n"] != 1 || r["text"] != "悴んだ心ふるえる眼差し" || f.responded != 1 {
		t.Errorf("answered %v, responded %d", f.answers, f.responded)
	}
	if a.pl.SingingAlong() {
		t.Error("played again to sing along, the teacher on")
	}
	hear(make([]float32, 4800))
	if f.heard != 1600 {
		t.Error("heard as the teacher answers")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	hear(make([]float32, 4800))
	if f.heard != 3200 {
		t.Error("deaf once the teacher is done")
	}
	if said := a.teacher.said; len(said) != 1 || !said[0].teacher || said[0].text != "我们来听第一句" {
		t.Errorf("said %+v", said)
	}
}

// The teacher's calls wait for its voice to be heard first, so the music
// does not play over it.
func TestTeacherCallsWaitForItsVoice(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Speech, Audio: make([]float32, teacher.OutRate)})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if a.pl.Playing() || len(f.answers) != 0 {
		t.Errorf("played the line over the teacher's voice: playing %v, answered %v", a.pl.Playing(), f.answers)
	}
}

// Asked to record, the app records the listener singing the line, which
// the teacher hears; once they are heard to stop, the take ends and is
// kept, not played back, and the teacher can play it.
func TestTeacherRecordsLine(t *testing.T) {
	a, _, m, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	r, _ := f.answers["r"].(map[string]any)
	if a.lesson.takes.rec == nil || r["n"] != 2 {
		t.Fatalf("recording %v, answered %v", a.lesson.takes.rec != nil, f.answers)
	}
	m.sing(2 * time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "世界で僕は独りぼっちだった"})
	tk := a.lesson.takes
	if tk.rec != nil || len(tk.sung[1].samples) != 2*48000 || tk.out != nil {
		t.Fatalf("heard to stop: recording %v, kept %d, playing back %v", tk.rec != nil, len(tk.sung[1].samples), tk.out != nil)
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "p", Name: "play_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if a.lesson.takes.out == nil || !a.pl.PlayingUnder() {
		t.Error("the teacher could not play the take")
	}
	if _, answered := f.answers["p"]; answered {
		t.Error("answered before the take played")
	}
}

// The teacher moves the lesson's line without playing it, and hears why
// a call to a line there is not came to nothing.
func TestTeacherPointsAtLines(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	lines, _ := a.pl.Lyrics()
	call := func(name string, args map[string]any) map[string]any {
		f.answers = map[string]any{}
		a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: name, Name: name, Args: args}})
		a.teacherEvent(teacher.Event{Kind: teacher.Answered})
		a.teacherTick()
		a.teacherEvent(teacher.Event{Kind: teacher.Answered})
		r, _ := f.answers[name].(map[string]any)
		return r
	}
	if r := call("previous_line", nil); r["n"] != 1 || a.lessonAt(lines) != 0 {
		t.Errorf("previous line: %v, at %d", r, a.lessonAt(lines))
	}
	if r := call("previous_line", nil); r["error"] == nil {
		t.Errorf("before the first line: %v", r)
	}
	if r := call("highlight_line", map[string]any{"n": 2.0}); r["n"] != 2 || a.pl.Playing() {
		t.Errorf("highlighted %v, playing %v", r, a.pl.Playing())
	}
	if r := call("play_line", map[string]any{"n": 9.0}); r["error"] == nil || a.pl.Playing() {
		t.Errorf("played line 9 of 2: %v", r)
	}
}
