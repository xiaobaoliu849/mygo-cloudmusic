package app

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
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
	cancelled    int
}

func (f *fakeTeacher) Cancel() error { f.cancelled++; return nil }

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
// fake, and hear giving the microphone, the fake one, samples.
func teacherApp(t *testing.T) (*app, *ui.Tester, *fakeMic, *fakeTeacher, func([]float32)) {
	t.Helper()
	a, tt, m := takeApp(t)
	f := &fakeTeacher{answers: map[string]any{}}
	hear := func(s []float32) { m.got(s) }
	oldKey, oldDial := teacherKey, dialTeacher
	teacherKey = func(string) string { return "test-key" }
	dialed := make(chan teacher.Config, 1)
	dialTeacher = func(cfg teacher.Config, got func(teacher.Event)) (teacherSession, error) {
		dialed <- cfg
		return f, nil
	}
	t.Cleanup(func() { teacherKey, dialTeacher = oldKey, oldDial })
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
	teacherKey = func(string) string { return "" }
	var saved string
	saveTeacherKey = func(_, key string) error { saved = key; return nil }
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

	// Turned on again, it starts a new conversation, showing none of the
	// last: it greeted twice, the first greeting kept.
	a.teacher.said = []utterance{{teacher: true, text: "你好"}}
	teacherKey = func(string) string { return saved }
	tt.Click("AI 老师")
	tt.Frame()
	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("did not connect again")
	}
	if len(a.teacher.said) != 0 {
		t.Errorf("turned on again, showing %v", a.teacher.said)
	}
}

// A line the teacher plays plays once, with no pass to sing along, and
// the teacher hears what it came to only once it has played: until
// then, and while the teacher answers, the microphone does not go to it.
func TestTeacherPlaysLine(t *testing.T) {
	a, _, _, f, hear := teacherApp(t)
	a.teacherTick()
	voice := make([]float32, 4800)
	for i := range voice {
		voice[i] = 0.3
	}
	hear(voice)
	if f.heard != 1600 {
		t.Fatalf("heard %d samples of 100 ms, want 1600 at 16 kHz", f.heard)
	}
	if a.teacher.level.Load() == 0 {
		t.Error("the meter shows nothing heard")
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "我们来听第一句"})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if n, alone := a.pl.Line(); n != 0 || !alone || !a.pl.Playing() {
		t.Fatalf("line %d, alone %v, playing %v", n, alone, a.pl.Playing())
	}
	hear(voice)
	if len(f.answers) != 0 || f.heard != 1600 || a.teacher.level.Load() != 0 {
		t.Errorf("as the line plays: answered %v, heard %d, meter %v", f.answers, f.heard, a.teacher.level.Load())
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
	// Its voice heard, the line plays. It used to wait on forever, the
	// voice's player stopping a few frames short of the end of it.
	for deadline := time.Now().Add(3 * time.Second); !a.pl.Playing(); {
		if time.Now().After(deadline) {
			t.Fatalf("the line did not play once the teacher's voice ended: speaking %v", a.teacherSpeaking())
		}
		time.Sleep(50 * time.Millisecond)
		a.teacherTick()
	}
}

// Asked to record, the app records the listener singing the line, which
// the teacher hears; once they are heard to stop, the take ends and is
// kept, not played back, and the teacher can play it.
func TestTeacherRecordsLine(t *testing.T) {
	a, _, m, f, _ := teacherApp(t)
	responded := f.responded
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	r, _ := f.answers["r"].(map[string]any)
	if a.lesson.takes.rec == nil || r["n"] != 2 {
		t.Fatalf("recording %v, answered %v", a.lesson.takes.rec != nil, f.answers)
	}
	// The teacher is asked to go on as the take begins, or what it is told
	// piles up until the model refuses more; what it answers goes, as it
	// judged the line before it was sung.
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "唱得很好"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	if f.responded != responded+1 || f.cancelled != 1 || a.lesson.takes.rec == nil {
		t.Fatalf("as the take began: responded %d, cancelled %d, recording %v", f.responded-responded, f.cancelled, a.lesson.takes.rec != nil)
	}
	for _, u := range a.teacher.said {
		if u.teacher && u.text != "" {
			t.Fatalf("the teacher said %q as the take began", u.text)
		}
	}
	m.sing(2 * time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "世界で僕は独りぼっちだった"})
	tk := a.lesson.takes
	if tk.rec != nil || len(tk.sung[1].samples) != 2*48000 || tk.out != nil {
		t.Fatalf("heard to stop: recording %v, kept %d, playing back %v", tk.rec != nil, len(tk.sung[1].samples), tk.out != nil)
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
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

// The listener starting a line themselves while the teacher answers cuts
// the teacher short: its response is cancelled, and the line it was
// about to play is skipped, rather than stopping theirs once its voice
// ends, and it waits for them to speak.
func TestTeacherYields(t *testing.T) {
	a, tt, _, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "我们先来听第二句"})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 2.0}}})
	if err := tt.Click("悴んだ心ふるえる眼差し"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.cancelled != 1 {
		t.Errorf("cancelled %d times", f.cancelled)
	}
	// What is left of the cut response goes.
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "，注意换气"})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c2", Name: "pause"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if n, alone := a.pl.Line(); n != 0 || !alone || !a.pl.Playing() {
		t.Errorf("the listener's line: line %d, alone %v, playing %v", n, alone, a.pl.Playing())
	}
	for _, id := range []string{"c1", "c2"} {
		if r, _ := f.answers[id].(map[string]any); r["skipped"] == nil {
			t.Errorf("%s answered %v", id, f.answers[id])
		}
	}
	if f.responded != 0 {
		t.Error("the teacher went on, not waiting for the listener")
	}
	if said := a.teacher.said; said[len(said)-1].text != "我们先来听第二句" {
		t.Errorf("said %q", said[len(said)-1].text)
	}
}

// The lesson numbers the lines sung from 1, leaving out the credits, as
// it leaves them out of its list, and the teacher goes by the same
// numbers. They were numbered by their place among all the lines: after
// 作词, 作曲 and 编曲, the first line sung was 4. The line before the first
// sung is none, not one of the credits.
func TestLessonNumbersSungLines(t *testing.T) {
	a, tt, _ := takeApp(t)
	a.pl.SetLyrics([]lyrics.Line{
		{At: 0, End: time.Second, Text: "作词 : SoulJa/青山テルマ"},
		{At: time.Second, End: 2 * time.Second, Text: "作曲 : SoulJa"},
		{At: 2 * time.Second, End: 3 * time.Second, Text: "编曲 : Hiroshi Sato"},
		{At: 10 * time.Second, End: 13 * time.Second, Text: "あなたのこと私は今でも"},
		{At: 13 * time.Second, End: 17 * time.Second, Text: "思い続けているよ"},
	})
	tt.Frame()
	if err := tt.Click("あなたのこと私は今でも"); err != nil {
		t.Fatal(err)
	}
	a.pl.StopLine()
	tt.Frame()
	texts := " " + strings.Join(tt.Texts(), " ") + " "
	if !strings.Contains(texts, " 1 ") || !strings.Contains(texts, " 2 ") || strings.Contains(texts, " 4 ") || !strings.Contains(texts, "第 1 句") {
		t.Errorf("numbered the lines %s", texts)
	}

	tt.Click("上一句")
	tt.Frame()
	if n, _ := a.pl.Line(); n != 3 {
		t.Errorf("the line before the first sung went to line %d", n)
	}

	lines, _ := a.pl.Lyrics()
	told := a.teacherInstructions()
	if !strings.Contains(told, "1. あなたのこと私は今でも") || !strings.Contains(told, "2. 思い続けているよ") || strings.Contains(told, "4. あなたのこと") {
		t.Errorf("told the teacher %s", told)
	}
	if n, ok := teacherLine(lines, map[string]any{"n": 1.0}, 0); !ok || n != 3 || lineFor(lines, n)["n"] != 1 {
		t.Errorf("the teacher's line 1 is line %d, numbered %v", n, lineFor(lines, n)["n"])
	}
	if _, ok := teacherLine(lines, map[string]any{"n": 3.0}, 0); ok {
		t.Error("the teacher has a line 3 of 2")
	}
}

// A stray sound as the listener begins, heard as "To.", neither ends the
// take nor is answered: it ended the take, and the teacher judged it as
// the line.
func TestTeacherIgnoresStraySound(t *testing.T) {
	a, _, m, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	// Its answer as the take begins goes.
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	f.cancelled = 0
	m.sing(time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "To."})
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	if a.lesson.takes.rec == nil || f.cancelled != 1 {
		t.Fatalf("after a stray sound: recording %v, cancelled %d", a.lesson.takes.rec != nil, f.cancelled)
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "世界で僕は独りぼっちだった"})
	if a.lesson.takes.rec != nil {
		t.Error("the line sung did not end the take")
	}
}

// The teacher and a take share the microphone, opened once: a take the
// teacher asks for hears the listener as the teacher does, and once it
// ends, the teacher hears on. Each opened the microphone of its own, and
// a take came out silent, the teacher deaf after it.
func TestTeacherSharesMic(t *testing.T) {
	a, _, m, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if a.lesson.takes.rec == nil || m.opens != 1 {
		t.Fatalf("recording %v, the microphone opened %d times", a.lesson.takes.rec != nil, m.opens)
	}
	m.sing(time.Second)
	if f.heard != 16000 {
		t.Errorf("the teacher heard %d samples of a second", f.heard)
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "世界で僕は独りぼっちだった"})
	tk := a.lesson.takes.sung[1]
	if tk == nil || len(tk.samples) != 48000 || mic.Level(tk.samples) == 0 {
		t.Fatalf("the take came out %v", tk)
	}
	if m.stopped {
		t.Fatal("the take, ending, stopped the microphone the teacher hears")
	}
	m.sing(100 * time.Millisecond)
	if f.heard != 17600 {
		t.Errorf("after the take, the teacher heard %d, want 17600", f.heard)
	}
	a.stopTeacher()
	if !m.stopped {
		t.Error("the microphone records on, nothing listening")
	}
}

// The teacher saying it plays a line, and calling nothing, as it did
// ("好，那我再放一遍第二句给你听", and nothing played), is reminded once,
// unseen, to call it; asking whether to play is no promise, and once
// reminded, it is not again until it calls something or the listener
// speaks.
func TestTeacherRemindedToPlay(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	reply := func(text string, calls ...string) {
		a.teacherEvent(teacher.Event{Kind: teacher.Answering})
		a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: text})
		for _, name := range calls {
			a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: name, Name: name}})
		}
		a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	}
	told := len(f.told)
	reply("要不要我再放一遍？")
	if len(f.told) != told {
		t.Errorf("reminded, asking: %q", f.told[told:])
	}
	reply("好，那我再放一遍第二句给你听。")
	if len(f.told) != told+1 || !strings.Contains(f.told[told], "play_line") || f.responded != 1 {
		t.Fatalf("not reminded to play: told %q, responded %d", f.told[told:], f.responded)
	}
	if said := a.teacher.said[len(a.teacher.said)-1].text; strings.Contains(said, "提醒") {
		t.Error("the reminder shows as said")
	}
	reply("好，我放给你听。")
	if len(f.told) != told+1 {
		t.Error("reminded twice running")
	}
	reply("我们先听第二句。", "play_line")
	reply("好，再放一遍。")
	if len(f.told) != told+2 {
		t.Error("not reminded again, once it had called a tool")
	}
}

// What went wrong with a take is told until the listener starts on
// something else: it stayed, a take of long before seeming to go wrong
// on every line.
func TestTakeErrorClears(t *testing.T) {
	a, tt, _ := takeApp(t)
	a.lesson.takes.err = "没录到声音（5.0 秒，完全无声）"
	if err := tt.Click("悴んだ心ふるえる眼差し"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.lesson.takes.err != "" {
		t.Errorf("still telling %q", a.lesson.takes.err)
	}
}

// The listener heard and not answered, as they were ("Play.", and the
// teacher said nothing, the microphone open), is answered: the teacher is
// asked to, once, if it has not started to by unanswered; and a response
// that never comes is waited for no more, the microphone opening again.
func TestTeacherDoesNotStall(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	// A while after the teacher greeted the listener.
	now := time.Now().Add(time.Minute)
	old := teacherNow
	teacherNow = func() time.Time { return now }
	defer func() { teacherNow = old }()
	wait := func(d time.Duration) {
		now = now.Add(d)
		a.teacherTick()
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "Play."})
	wait(time.Second)
	if f.responded != 0 {
		t.Fatal("asked to answer at once")
	}
	wait(unanswered)
	if f.responded != 1 || !a.teacher.answering {
		t.Fatalf("not asked to answer: responded %d", f.responded)
	}
	// It answers nothing: it is asked no more, the listener having said
	// nothing since.
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	wait(unanswered + time.Second)
	if f.responded != 1 {
		t.Errorf("asked again: responded %d", f.responded)
	}
	// Answering the listener, of itself, with nothing, it is asked at
	// once; what they said written down only after, it is not.
	wait(time.Minute)
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "下一句"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	wait(100 * time.Millisecond)
	if f.responded != 2 {
		t.Errorf("not asked after answering nothing: responded %d", f.responded)
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "好，下一句。"})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c", Name: "next_line"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "好的"})
	a.teacherTick() // runs the call, and asks the teacher to go on
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "这一句是……"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	responded := f.responded
	wait(unanswered + time.Second)
	if f.responded != responded {
		t.Errorf("asked about what was written down late: responded %d, then %d", responded, f.responded)
	}

	// Speaking on after a pause, the listener is not answered over.
	wait(time.Minute)
	responded = f.responded
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "我觉得"})
	wait(time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Hearing, Text: "这一句"})
	wait(unanswered + time.Second)
	if f.responded != responded {
		t.Errorf("asked to answer while the listener spoke on: responded %d, then %d", responded, f.responded)
	}

	// Answered in time, it is not asked.
	wait(time.Minute)
	responded = f.responded
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "再来一遍"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	wait(unanswered + time.Second)
	if f.responded != responded {
		t.Errorf("asked while answering: responded %d, then %d", responded, f.responded)
	}
	// Its response goes quiet halfway: the app stops waiting for it.
	wait(stuckAnswering + time.Second)
	if a.teacher.answering {
		t.Error("still waiting on a response that went quiet")
	}
}

// A response asked for that never begins is asked for again, once, a few
// seconds on: the model let a reminder go by, and the listener waited
// 15 seconds in silence.
func TestTeacherAsksAgainForResponseNotBegun(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	now := time.Now().Add(time.Minute)
	old := teacherNow
	teacherNow = func() time.Time { return now }
	defer func() { teacherNow = old }()
	wait := func(d time.Duration) {
		now = now.Add(d)
		a.teacherTick()
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "我放慢了，再听一遍。"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	if f.responded != 1 {
		t.Fatalf("not reminded: responded %d", f.responded)
	}
	wait(time.Second)
	wait(unbegun + time.Second)
	if f.responded != 2 {
		t.Fatalf("not asked again: responded %d", f.responded)
	}
	wait(unbegun + time.Second)
	if f.responded != 2 {
		t.Errorf("asked more than once: responded %d", f.responded)
	}

	// One the listener cut short, by taking over, is not asked for again:
	// it would talk over what they do.
	wait(stuckAnswering + time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "该你唱了"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	responded := f.responded
	a.teacherYield()
	wait(unbegun + time.Second)
	if f.responded != responded {
		t.Errorf("asked again after the listener took over: responded %d, then %d", responded, f.responded)
	}
}

// A take the teacher asked for, with nothing sung, neither has the teacher
// answer the room's noise while it goes on, nor move on once it runs out:
// told the line was not sung, it has the listener sing it again. Gemini
// moved on to the next line when nothing was sung.
func TestTeacherSilentTake(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	// Its answer as the take begins on its way, it hears the singing: it
	// missed the start of the line, deaf until that answer was done.
	a.teacherTick()
	if !a.teacher.open.Load() {
		t.Error("deaf as the take began")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	// An answer to nothing heard, as the take goes on.
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	if !a.teacher.hushed {
		t.Error("answered the room's noise as the take went on")
	}
	// The take runs out as that answer goes on: it is told once it ends.
	responded := f.responded
	a.stopRecording() // as it does, run out
	a.teacherTick()
	if f.responded != responded {
		t.Error("told the line was not sung as it answered something else")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if f.responded != responded+1 || len(f.told) == 0 || f.told[len(f.told)-1] != silentTake {
		t.Fatalf("not told the line was not sung: responded %d, told %q", f.responded-responded, f.told)
	}
	a.teacherTick()
	if f.responded != responded+1 {
		t.Error("told more than once")
	}
}

// The teacher's answer to the line sung, begun before what was sung is
// written down, as DashScope does at times, is not taken for an answer to
// noise: some of the line was heard as it went.
func TestTeacherAnswersSingingWrittenDownLate(t *testing.T) {
	a, _, m, _, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	m.sing(2 * time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Hearing, Text: "世界で僕は"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	if a.teacher.hushed {
		t.Error("the answer to the line sung was dropped")
	}
}

// The answer to a take beginning, waited for in vain, is not dropped in
// place of the answer to the line sung: the teacher said nothing of it.
func TestTeacherDropsNoLaterAnswer(t *testing.T) {
	a, _, m, _, _ := teacherApp(t)
	now := time.Now().Add(time.Minute)
	old := teacherNow
	teacherNow = func() time.Time { return now }
	defer func() { teacherNow = old }()
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "r", Name: "start_recording"}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	// Its answer as the take begins never comes.
	for range 3 {
		now = now.Add(stuckAnswering / 2)
		a.teacherTick()
	}
	m.sing(2 * time.Second)
	a.teacherEvent(teacher.Event{Kind: teacher.Heard, Text: "世界で僕は独りぼっちだった"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	if a.teacher.hushed {
		t.Error("the answer to the line sung was dropped")
	}
}

// "好，下一句。" with nothing called is reminded to move on: it was not,
// and the listener waited in silence; asking which, it is not.
func TestTeacherRemindedToMoveOn(t *testing.T) {
	if !promisesToPlay("好，下一句。") || promisesToPlay("再来一遍，还是下一句？") {
		t.Error("moving on, or asking whether to, taken the wrong way")
	}
	// Speaking English, the same.
	if !promisesToPlay("Nice! Let's move on to the next line.") || promisesToPlay("Want to try the next line?") {
		t.Error("moving on in English, or asking whether to, taken the wrong way")
	}
}

// Gemini hears the listener as it thinks: they spoke, and it was deaf,
// up to 15 s, as it chose to say nothing. What plays it still does not
// hear, through speakers, as the listener.
func TestGeminiHearsAsItThinks(t *testing.T) {
	a, _, _, f, hear := teacherApp(t)
	a.settings.TeacherProvider = teacher.Gemini
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherTick()
	hear(make([]float32, 4800))
	if f.heard == 0 {
		t.Fatal("deaf as the teacher thinks")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	heard := f.heard
	hear(make([]float32, 4800))
	if !a.pl.Playing() || f.heard != heard {
		t.Errorf("as the line plays through speakers: playing %v, heard %d more", a.pl.Playing(), f.heard-heard)
	}

	// The line played, it thinks again, hearing; its voice arriving, it
	// stops hearing at once, not at the next tick: it heard the start of
	// itself, and took it for the listener cutting in.
	a.pl.StopLine()
	a.teacherTick()
	a.teacherTick()
	heard = f.heard
	hear(make([]float32, 4800))
	if f.heard == heard {
		t.Fatal("deaf as the teacher thinks on")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Speech, Audio: make([]float32, teacher.OutRate/10)})
	heard = f.heard
	hear(make([]float32, 4800))
	if f.heard != heard {
		t.Error("heard its own voice begin")
	}
}

// Spoken over, the teacher stops at once, and what it calls in what was
// left of its answer does not run; a call it takes back stops what it
// plays, and is not answered.
func TestTeacherInterrupted(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	a.settings.TeacherProvider = teacher.Gemini
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Speech, Audio: make([]float32, teacher.OutRate)})
	a.teacherEvent(teacher.Event{Kind: teacher.Interrupted})
	if a.teacherSpeaking() {
		t.Error("spoke on over the listener")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if a.pl.Playing() {
		t.Error("played the line called after it was spoken over")
	}

	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c2", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	if !a.pl.Playing() {
		t.Fatal("did not play the line")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Withdrawn, Call: teacher.Call{ID: "c2"}})
	a.teacherTick()
	if a.pl.Playing() || f.answers["c2"] != nil {
		t.Errorf("a call taken back: playing %v, answered %v", a.pl.Playing(), f.answers["c2"])
	}
}

// Wearing headphones, the listener speaks over the line the teacher plays:
// it stops, and the teacher is told, to hear them out. Singing along with
// it, it plays on.
func TestTeacherBargeIn(t *testing.T) {
	a, _, _, f, hear := teacherApp(t)
	a.settings.TeacherProvider = teacher.Gemini
	a.settings.Headphones = true
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Calling, Call: teacher.Call{ID: "c1", Name: "play_line", Args: map[string]any{"n": 1.0}}})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	a.teacherTick()
	hear(make([]float32, 4800))
	if !a.pl.Playing() || f.heard == 0 {
		t.Fatalf("as the line plays: playing %v, heard %d", a.pl.Playing(), f.heard)
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Hearing, Text: "悴んだ心ふる"})
	a.teacherTick()
	if !a.pl.Playing() {
		t.Fatal("stopped as the listener sang along")
	}
	a.teacherEvent(teacher.Event{Kind: teacher.Hearing, Text: "等一下，这个词怎么念"})
	a.teacherTick()
	a.teacherTick()
	r, _ := f.answers["c1"].(map[string]any)
	if a.pl.Playing() || r["interrupted"] != bargedIn {
		t.Errorf("spoken over: playing %v, answered %v", a.pl.Playing(), f.answers["c1"])
	}
}

// A promise to sing the line again, as "好，我们再来一遍第7句。", with
// nothing called, is reminded to call it: it was not, and nothing played.
func TestTeacherRemindedToSingAgain(t *testing.T) {
	a, _, _, f, _ := teacherApp(t)
	a.teacherEvent(teacher.Event{Kind: teacher.Answering})
	a.teacherEvent(teacher.Event{Kind: teacher.Saying, Text: "好，我们再来一遍第7句。"})
	a.teacherEvent(teacher.Event{Kind: teacher.Answered})
	if len(f.told) != 2 || f.responded != 1 {
		t.Errorf("not reminded: told %q, responded %d", f.told, f.responded)
	}
}
