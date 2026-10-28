package app

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
	"netease-music-mygo/internal/player"
	"netease-music-mygo/internal/teacher"
)

// A teacherSession is the conversation with the AI teacher: the model's,
// or a test's.
type teacherSession interface {
	Hear(samples []float32) error
	Instruct(instructions string) error
	Tell(text string) error
	Answer(id string, result any) error
	Respond() error
	Cancel() error
	Close()
}

// dialTeacher connects to the teacher; tests swap it.
var dialTeacher = func(cfg teacher.Config, got func(teacher.Event)) (teacherSession, error) {
	if cfg.Provider == teacher.Gemini {
		s, err := teacher.DialGemini(cfg, got)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	s, err := teacher.Dial(cfg, got)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// teacherSecret is what the API key of provider is kept as, in the
// system's credential store, and teacherEnv what it may be set as in the
// environment instead.
func teacherSecret(provider string) string {
	if provider == teacher.Gemini {
		return "gemini-api-key"
	}
	return "dashscope-api-key"
}

func teacherEnv(provider string) string {
	if provider == teacher.Gemini {
		return "GEMINI_API_KEY"
	}
	return "DASHSCOPE_API_KEY"
}

// teacherKey is the API key of provider the teacher connects with: from
// the environment if it is set there, else as the listener pasted it, kept
// in the system's credential store rather than in the app's files.
var teacherKey = func(provider string) string {
	if key := os.Getenv(teacherEnv(provider)); key != "" {
		return key
	}
	b, err := mygo.Secrets.Get(teacherSecret(provider))
	if err != nil {
		return ""
	}
	return string(b)
}

// saveTeacherKey keeps the key of provider the listener pasted; tests swap
// it.
var saveTeacherKey = func(provider, key string) error {
	return mygo.Secrets.Set(teacherSecret(provider), []byte(key))
}

// teacherState is the AI teacher: a voice model the listener talks with,
// which runs the lesson through the tools the app gives it, playing a
// line, then hearing the listener sing it, and saying how it went.
type teacherState struct {
	on   bool           // the listener turned the teacher on
	ask  bool           // it asks for the API key, with none, or to change it
	key  string         // the key being pasted
	gen  int            // counts sessions, to drop what an ended one sends
	sess teacherSession // nil while it connects
	err  string         // why it went wrong, to show

	said      []utterance // the conversation, oldest first
	hearing   string      // what the listener says so far, to be revised
	saying    bool        // the teacher's last utterance goes on
	answering bool        // the teacher is responding
	// hushed drops what is left of a response the listener cut short, by
	// taking over, until the next.
	hushed bool
	// drop says the teacher's next response, still to begin, is to be
	// dropped: its answer to a take beginning, or to a stray sound in one.
	drop bool
	// take says a take the teacher asked for goes on, the line not yet
	// heard sung.
	take bool
	// reply is what the teacher says in the response on its way, and
	// replyCalls how many tools it calls in it.
	reply      string
	replyCalls int
	// reminded says the teacher was reminded to call what it said it
	// would play, once, until it calls something or the listener speaks.
	reminded bool

	// calls are the response's calls of tools: run in turn once it is
	// done and heard, each once what the one before started has ended.
	calls []*teacherCall

	song  int64 // the track whose lyrics the teacher was told
	voice *teacher.Voice
	out   *audio.Player // plays voice
	mic   interface{ Stop() }
	open  *atomic.Bool // the microphone goes to the teacher
	// level is how loud the teacher hears the listener, as mic.Level, in
	// the bits of a float32: 0 while it does not hear.
	level *atomic.Uint32
	quit  chan struct{}

	log *teacherLog // nil without a window, as in tests
	// spoke is when the listener was heard last, unanswered so far, and
	// nudged says the teacher was asked once since to answer them.
	spoke  time.Time
	nudged bool
	// answeringSpoke is when the listener spoke whom the teacher answers
	// now, zero for a response the app asked for, and answeredAt when it
	// last finished answering.
	answeringSpoke, answeredAt time.Time
	// lastHeard is when the teacher last sent anything, or was asked to
	// answer; asked says it was answering at the last tick.
	lastHeard time.Time
	asked     bool
	// begun says the response the teacher gives now has begun; retried,
	// that one asked for and never begun was asked for again.
	begun, retried bool
}

// teacherNow is the time, for the teacher's watchdogs; tests swap it.
var teacherNow = time.Now

const (
	// unanswered is how long the teacher may take to start answering the
	// listener before it is asked to: turn detection sometimes hears them,
	// writes them down and answers nothing, and they wait on in silence.
	unanswered = 4 * time.Second
	// stuckAnswering is how long the teacher may answer with nothing from
	// it before the app stops waiting: a response asked for that never
	// comes would keep the microphone shut.
	stuckAnswering = 15 * time.Second
	// unbegun is how long a response asked for may take to begin before it
	// is asked for again, once: the model sometimes lets the asking go by,
	// and the listener waited out stuckAnswering in silence.
	unbegun = 4 * time.Second
	// lateHeard is how long after an answer what the listener said may
	// still be written down, the answer to it.
	lateHeard = 2 * time.Second
)

// An utterance is something the teacher or the listener said.
type utterance struct {
	teacher bool
	text    string
}

// A teacherCall is the teacher calling one of the app's tools: what it
// came to, and, for what plays, whether it plays on, to tell the teacher
// once it ends.
type teacherCall struct {
	teacher.Call
	ran     bool
	skipped bool // not run: the listener took over
	result  any
	busy    func() bool
}

// skippedCall is what a call the listener took over from comes to.
func skippedCall() map[string]any {
	return map[string]any{"skipped": "学生自己操作了播放器，这一步没做；等学生说话再接着教"}
}

// teacherYield has the teacher give way to the listener, who started or
// stopped something in the lesson themselves: its voice stops, what is
// left of its response goes, and its calls are skipped, so that they do
// not play over what the listener does, nor stop it.
func (a *app) teacherYield() {
	t := &a.teacher
	if t.sess == nil {
		return
	}
	t.voice.Hush()
	if t.answering {
		t.sess.Cancel()
		t.hushed = true
	}
	for _, c := range t.calls {
		if !c.ran {
			c.ran, c.skipped, c.result = true, true, skippedCall()
		}
		c.busy = nil
	}
	// A take the teacher asked for, the listener stopped or started over.
	t.saying, t.take = false, false
}

// startTeacher connects to the teacher, or, with no API key, asks for one.
func (a *app) startTeacher() {
	key := teacherKey(a.settings.TeacherProvider)
	if key == "" {
		a.stopTeacher()
		a.teacher.ask, a.teacher.err = true, ""
		return
	}
	a.connectTeacher(key)
}

// pasteTeacherKey keeps the key the listener pasted, and connects with it.
func (a *app) pasteTeacherKey() {
	t := &a.teacher
	key := strings.TrimSpace(t.key)
	if key == "" {
		return
	}
	t.key = ""
	err := saveTeacherKey(a.settings.TeacherProvider, key)
	a.connectTeacher(key)
	if err != nil {
		t.err = "API Key 存不进系统的凭据管理器，这次能用，下次还要再粘贴：" + err.Error()
	}
}

// connectTeacher connects to the teacher with key, telling it the song
// playing.
func (a *app) connectTeacher(key string) {
	t := &a.teacher
	a.stopTeacher()
	// A new conversation: the teacher remembers none of the last.
	t.on, t.err, t.said = true, "", nil
	gen := t.gen
	t.song = a.lyricsTrack()
	cfg := teacher.Config{Provider: a.settings.TeacherProvider, Key: key, Instructions: a.teacherInstructions(), Tools: teacherTools}
	got := func(e teacher.Event) {
		a.post(func() {
			if a.teacher.gen == gen {
				a.teacherEvent(e)
			}
		})
	}
	go func() {
		s, err := dialTeacher(cfg, got)
		a.post(func() { a.teacherDialed(gen, s, err) })
	}()
}

// teacherDialed starts the session of gen once it connects: the teacher's
// voice playing, the microphone going to it.
func (a *app) teacherDialed(gen int, s teacherSession, err error) {
	t := &a.teacher
	if gen != t.gen || !t.on {
		if s != nil {
			s.Close()
		}
		return
	}
	if err != nil {
		t.on, t.err = false, "连不上 AI 老师："+err.Error()
		return
	}
	if a.win != nil {
		t.log = openTeacherLog()
		t.log.printf("provider %s", cmp.Or(a.settings.TeacherProvider, "dashscope"))
		s = loggedSession{s, t.log}
	}
	t.sess = s
	t.voice = teacher.NewVoice()
	t.out = audio.NewPlayer(t.voice)
	t.out.Play()
	open, level := &atomic.Bool{}, &atomic.Uint32{}
	t.open, t.level = open, level
	down := mic.NewDownsampler(teacher.InRate)
	m, err := a.microphone().listen(func(samples []float32) {
		if !open.Load() {
			level.Store(0)
			return
		}
		level.Store(math.Float32bits(mic.Level(samples)))
		s.Hear(down.Next(samples))
	})
	if err != nil {
		t.err = "麦克风打不开：" + err.Error()
	}
	t.mic = m
	t.quit = make(chan struct{})
	go a.teacherTicks(gen, t.quit)
	// The teacher speaks first: it answers nothing before it hears
	// something, so it hears the listener came in.
	s.Tell(a.teacherHello())
	s.Respond()
	t.answering = true
}

// teacherHello tells the teacher the listener turned it on, and where.
func (a *app) teacherHello() string {
	lines, _ := a.pl.Lyrics()
	if len(lines) == 0 {
		return "（学生刚打开 AI 老师，还没有放有歌词的歌。简短地打个招呼，请他先放一首。）"
	}
	return fmt.Sprintf("（学生刚打开 AI 老师，歌词停在第 %d 句。简短地打个招呼，然后开始教这一句。）", lineNumber(lines, a.lessonAt(lines)))
}

// teacherTicks has the app look in on the teacher ten times a second,
// until quit.
func (a *app) teacherTicks(gen int, quit chan struct{}) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-quit:
			return
		case <-tick.C:
			a.post(func() {
				if a.teacher.gen == gen {
					a.teacherTick()
				}
			})
		}
	}
}

// stopTeacher ends the session, keeping what was said, to read.
func (a *app) stopTeacher() {
	t := &a.teacher
	if t.quit != nil {
		close(t.quit)
	}
	if t.mic != nil {
		t.mic.Stop()
	}
	if t.sess != nil {
		t.sess.Close()
	}
	if t.voice != nil {
		// Closed first, it lets go of the player waiting on it.
		t.voice.Close()
	}
	if t.out != nil {
		t.out.Close()
	}
	t.log.close()
	*t = teacherState{gen: t.gen + 1, said: t.said, err: t.err}
}

// teacherEvent hears what the teacher sends.
func (a *app) teacherEvent(e teacher.Event) {
	t := &a.teacher
	t.lastHeard = teacherNow()
	a.logTeacherEvent(e)
	switch e.Kind {
	case teacher.Hearing:
		t.hearing = e.Text
		// The listener speaks on: not to be answered before they finish.
		t.spoke = time.Time{}
	case teacher.Heard:
		t.hearing = ""
		if rec := a.lesson.takes.rec; rec != nil && a.strayHeard(e.Text, rec.key) {
			// A breath or a noise as the listener begins: the take goes on,
			// and the teacher's answer to it goes, now or as it begins.
			if t.answering && t.sess != nil {
				if t.voice != nil {
					t.voice.Hush()
				}
				t.sess.Cancel()
				t.hushed = true
			} else {
				t.drop = true
			}
			break
		}
		// The listener heard in earnest: what was to be dropped is past,
		// and the answer to this is not to go with it.
		t.drop, t.take = false, false
		if e.Text != "" {
			u := utterance{text: e.Text}
			if t.saying {
				// Written down as the teacher had started to answer: it goes
				// before the answer.
				t.said = append(t.said[:len(t.said)-1], u, t.said[len(t.said)-1])
			} else {
				t.said = append(t.said, u)
			}
		}
		t.reminded = false
		// Written down only after an answer to it, as happens, it was
		// answered.
		if e.Text != "" && !t.answering && teacherNow().Sub(t.answeredAt) > lateHeard {
			t.spoke, t.nudged = teacherNow(), false
		}
		// The listener stopped singing: the take of it ends.
		if a.lesson.takes.rec != nil {
			a.stopRecording()
		}
	case teacher.Answering:
		t.answering, t.hushed, t.err = true, false, ""
		t.begun, t.retried = true, false
		t.reply, t.replyCalls = "", 0
		// An answer begun as the take goes on, nothing of the line heard,
		// is to nothing: Gemini answered the room's noise, moving on. With
		// some of it heard, it answers the singing, written down late.
		if (t.drop || t.take && a.lesson.takes.rec != nil && t.hearing == "") && t.sess != nil {
			t.drop = false
			t.sess.Cancel()
			t.hushed = true
		}
		t.answeringSpoke, t.spoke = t.spoke, time.Time{}
	case teacher.Saying:
		if t.hushed {
			break
		}
		if !t.saying {
			t.said = append(t.said, utterance{teacher: true})
			t.saying = true
		}
		t.said[len(t.said)-1].text += e.Text
		t.reply += e.Text
	case teacher.Speech:
		if t.voice != nil && !t.hushed {
			t.voice.Say(e.Audio)
		}
	case teacher.Calling:
		c := &teacherCall{Call: e.Call}
		if t.hushed {
			c.ran, c.skipped, c.result = true, true, skippedCall()
		}
		t.calls = append(t.calls, c)
		t.replyCalls++
	case teacher.Answered:
		t.answering, t.saying, t.begun = false, false, false
		if e.Err != nil {
			t.err = e.Err.Error()
		}
		if t.replyCalls > 0 {
			t.reminded = false
		} else if !t.hushed && !t.reminded && t.sess != nil && promisesToPlay(t.reply) {
			// It said it would play something, and called nothing, as it
			// sometimes does: reminded, once, it calls it.
			t.reminded = true
			t.sess.Tell(playReminder)
			t.sess.Respond()
			t.answering = true
		} else if t.reply == "" && t.replyCalls == 0 && !t.hushed && (e.Err != nil || !t.answeringSpoke.IsZero()) {
			// It answered the listener with nothing, as it sometimes does,
			// or its answer failed: they are still to be answered, and are
			// asked about at once, unless they were once already.
			t.spoke = teacherNow().Add(-unanswered)
		}
		t.answeredAt = teacherNow()
	case teacher.Failed:
		// A response asked for again while the one asked for first, slow
		// to begin, is still to come: nothing went wrong to show.
		if !strings.Contains(e.Err.Error(), "already has an active response") {
			t.err = e.Err.Error()
		}
	case teacher.Closed:
		if e.Err != nil {
			t.err = "和 AI 老师断开了：" + e.Err.Error()
		}
		a.stopTeacher()
	}
	if len(t.said) > 60 {
		t.said = t.said[len(t.said)-60:]
	}
}

// strayHeard reports whether what the teacher heard in the take of line
// key is too short to be the line sung, as "To." of a breath or a noise:
// fewer than three letters, and under half the line's.
func (a *app) strayHeard(text string, key int) bool {
	n := letterCount(text)
	if n >= 3 {
		return false
	}
	if lines, _ := a.pl.Lyrics(); key >= 0 && key < len(lines) {
		return 2*n < letterCount(lines[key].Text)
	}
	return true
}

// letterCount is how many letters text has, each kana or kanji one.
func letterCount(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// promisesToPlay reports whether what the teacher said says it plays
// something now, as "我再放一遍第二句给你听": not asks whether to, as
// "要不要我再放一遍？".
func promisesToPlay(said string) bool {
	said = strings.TrimSpace(said)
	if strings.HasSuffix(said, "？") || strings.HasSuffix(said, "?") {
		return false
	}
	return playPromise.MatchString(said)
}

var playPromise = regexp.MustCompile("(?i)再放|放一遍|放给你|放第|播放|听一遍|再听|来听|先听|听一下|示范|再来一遍|再来一次|来一遍|开始录|录一下|现在你来唱|轮到你|该你唱|该你了|下一句|" +
	`next line|next one|move on|listen (again|to)|let's (hear|listen|try|play)|play it|one more time|your turn`)

// silentTake tells the teacher the take ran out with nothing sung.
const silentTake = "（系统提醒，不是学生说的：录音时间到了，学生这一句没有唱出声。不要评价，也不要换下一句；简短、换个说法鼓励他跟着唱，然后用 play_line 再放这一句。）"

// playReminder reminds the teacher to call what it said it would play.
const playReminder = "（系统提醒，不是学生说的：你刚才说要放给学生听、请学生唱或换下一句，但没有调用工具。现在就调用相应的工具（play_line、loop_line、start_recording、play_recording，下一句先 next_line 再 play_line），不要再重复说话。）"

// teacherSpeaking reports whether the teacher's voice plays.
func (a *app) teacherSpeaking() bool {
	t := &a.teacher
	return t.voice != nil && t.out != nil && t.voice.Speaking(t.out.Position())
}

// teacherTick runs the teacher's calls once it has finished speaking,
// tells it the lyrics of another song, and lets it hear the microphone
// only while nothing plays: it has no ear for its own voice, or the
// music, apart from the listener's.
func (a *app) teacherTick() {
	t := &a.teacher
	if t.sess == nil {
		return
	}
	if song := a.lyricsTrack(); song != 0 && song != t.song {
		t.song = song
		t.sess.Instruct(a.teacherInstructions())
	}
	a.teacherWatch()
	recording := t.take && a.lesson.takes.rec != nil
	if t.take && a.lesson.takes.rec == nil && !t.answering {
		// The take ran out with the line not heard sung: told so, once
		// what it answers now is done, the teacher has them sing it again,
		// rather than moving on.
		t.take = false
		t.log.printf("the take ended unheard")
		t.sess.Tell(silentTake)
		t.sess.Respond()
		t.answering = true
	}
	speaking := a.teacherSpeaking()
	if !speaking && !t.answering {
		a.runCalls()
	}
	// In a take it asked for, it hears the singing even as an answer to
	// be dropped is on its way: it missed the start of the line, deaf
	// until that answer was done.
	t.open.Store(!speaking && (!t.answering || recording) && len(t.calls) == 0 && !a.pl.Playing() && a.lesson.takes.out == nil && !a.vocalsPlaying())
}

// teacherWatch keeps the conversation from stalling: the listener heard
// and not answered is answered, and a response that never comes is
// waited for no more, the microphone opening again.
func (a *app) teacherWatch() {
	t := &a.teacher
	now := teacherNow()
	if t.answering && !t.asked {
		t.lastHeard = now
	}
	t.asked = t.answering
	switch {
	case t.answering && !t.begun && !t.retried && !t.hushed && !t.drop && now.Sub(t.lastHeard) > unbegun:
		// Not one the listener cut short, nor one to be dropped: asked
		// again, it would talk over what they do, or over their singing.
		t.log.printf("the response did not begin: asking again")
		t.retried, t.lastHeard = true, now
		t.sess.Respond()
	case t.answering && now.Sub(t.lastHeard) > stuckAnswering:
		t.log.printf("nothing came of the response: listening again")
		t.answering, t.saying, t.asked, t.drop = false, false, false, false
	case !t.spoke.IsZero() && !t.answering && len(t.calls) == 0 && !t.nudged && now.Sub(t.spoke) > unanswered:
		t.log.printf("the listener was not answered: asking")
		t.spoke, t.nudged = time.Time{}, true
		t.sess.Respond()
		t.answering = true
	}
}

// logTeacherEvent logs e, but for the teacher's voice and what it hears as
// it goes: what it says is logged once said.
func (a *app) logTeacherEvent(e teacher.Event) {
	t := &a.teacher
	if t.log == nil {
		return
	}
	switch e.Kind {
	case teacher.Ready:
		t.log.printf("← ready")
	case teacher.Heard:
		t.log.printf("← heard %q", e.Text)
	case teacher.Answering:
		t.log.printf("← answering")
	case teacher.Calling:
		args, _ := json.Marshal(e.Call.Args)
		t.log.printf("← call %s %s %s", e.Call.ID, e.Call.Name, args)
	case teacher.Answered:
		t.log.printf("← answered %q, %d calls, err %v", t.reply, t.replyCalls, e.Err)
	case teacher.Failed:
		t.log.printf("← failed %v", e.Err)
	case teacher.Closed:
		t.log.printf("← closed %v", e.Err)
	}
}

// lyricsTrack is the track playing once its lyrics are loaded, else 0.
func (a *app) lyricsTrack() int64 {
	tr := a.pl.Current()
	if _, loaded := a.pl.Lyrics(); tr == nil || !loaded {
		return 0
	}
	return tr.ID
}

// runCalls runs the teacher's calls in turn, each once what the one
// before started has ended, then tells the teacher what they came to,
// for it to go on.
func (a *app) runCalls() {
	t := &a.teacher
	if len(t.calls) == 0 {
		return
	}
	for _, c := range t.calls {
		if !c.ran {
			c.ran = true
			c.result, c.busy = a.runTool(c.Call)
		}
		if c.busy != nil && c.busy() {
			return
		}
	}
	ran, recording := false, false
	for _, c := range t.calls {
		t.sess.Answer(c.ID, c.result)
		ran = ran || !c.skipped
		recording = recording || c.Name == "start_recording" && !c.skipped
	}
	t.calls = nil
	// Skipped, the teacher waits for the listener to speak.
	if ran {
		t.sess.Respond()
		t.answering = true
		// Recording, it waits for them to sing: its answer, as a take
		// begins, judged the line before it was sung, so it goes. Not
		// asked for at all, the results unanswered piled up until the
		// model refused more ("Turn queue overflow"). A take that could
		// not begin, the teacher is to say so.
		t.drop = recording && a.lesson.takes.rec != nil
	}
}

// toolError is what a tool that could not do what was asked comes to.
func toolError(why string) map[string]any { return map[string]any{"error": why} }

// runTool does what the teacher called c for, returning what it came to
// and, for what plays, whether it plays on.
func (a *app) runTool(c teacher.Call) (any, func() bool) {
	// What went wrong with a take before is told no more.
	a.lesson.takes.err = ""
	lines, _ := a.pl.Lyrics()
	if len(lines) == 0 && c.Name != "play_song" && c.Name != "pause" && c.Name != "slow_down" {
		return toolError("现在没有歌词：请学生先放一首有歌词的歌"), nil
	}
	at := a.lessonAt(lines)
	playing := func() bool { return a.pl.Playing() }
	switch c.Name {
	case "get_lyrics":
		return a.lyricsForTeacher(), nil
	case "play_line", "loop_line":
		n, ok := teacherLine(lines, c.Args, at)
		if !ok {
			return toolError(fmt.Sprintf("没有第 %v 句", c.Args["n"])), nil
		}
		a.stopRecording()
		a.stopTake()
		a.stopVocals()
		a.lesson.pointed = 0
		if c.Name == "play_line" {
			ok = a.pl.PlayLine(n)
		} else {
			times := 2
			if v, isNum := c.Args["times"].(float64); isNum {
				times = min(max(int(v), 1), 5)
			}
			ok = a.pl.LoopLine(n, times)
		}
		if !ok {
			return toolError("歌还没加载好，等一下再试"), nil
		}
		r := lineFor(lines, n)
		r["played"] = "播完了，已暂停"
		return r, playing
	case "play_song":
		a.stopTake()
		a.stopVocals()
		if !a.pl.Playing() {
			a.pl.TogglePlay()
		}
		return map[string]any{"playing": true, "note": "歌在放，学生暂停前你听不到他说话"}, nil
	case "pause":
		a.stopTake()
		a.stopVocals()
		if a.pl.Playing() {
			if _, alone := a.pl.Line(); alone || a.pl.SingingThrough() {
				a.pl.StopLine()
			} else {
				a.pl.TogglePlay()
			}
		}
		return map[string]any{"paused": true}, nil
	case "slow_down":
		rate := 0.75
		if v, ok := c.Args["rate"].(float64); ok {
			rate = v
		}
		rate = min(max(rate, 0.5), 1)
		a.settings.Tempo = rate
		a.pl.SetTempo(rate)
		a.settings.save()
		return map[string]any{"rate": a.pl.Tempo()}, nil
	case "highlight_line", "next_line", "previous_line":
		n, ok := at, true
		switch c.Name {
		case "highlight_line":
			n, ok = teacherLine(lines, c.Args, at)
		case "next_line":
			n, ok = stepLine(lines, at, 1)
		case "previous_line":
			n, ok = stepLine(lines, at, -1)
		}
		if !ok {
			return toolError("没有那一句了"), nil
		}
		a.lesson.pointed = n + 1
		return lineFor(lines, n), nil
	case "start_recording":
		n, ok := teacherLine(lines, c.Args, at)
		if !ok {
			return toolError(fmt.Sprintf("没有第 %v 句", c.Args["n"])), nil
		}
		if a.pl.Playing() {
			a.runTool(teacher.Call{Name: "pause"})
		}
		a.lesson.pointed = n + 1
		a.record(n, takeLimit(lines[n], a.pl.Tempo()), max(0, lines[n].At-player.LineLead))
		if a.lesson.takes.rec == nil {
			return toolError("录不了音：" + a.lesson.takes.err), nil
		}
		a.teacher.take = true
		r := lineFor(lines, n)
		r["recording"] = "录音已开始，学生在清唱：不要说话，安静地听，他唱完停下时录音自动结束"
		return r, nil
	case "stop_recording":
		a.stopRecording()
		return map[string]any{"stopped": true}, nil
	case "play_recording":
		n, ok := teacherLine(lines, c.Args, at)
		if !ok {
			return toolError(fmt.Sprintf("没有第 %v 句", c.Args["n"])), nil
		}
		if _, sung := a.lesson.takes.sung[n]; !sung {
			return toolError(fmt.Sprintf("第 %d 句还没有录音", lineNumber(lines, n))), nil
		}
		a.playTake(n)
		r := lineFor(lines, n)
		r["played"] = "学生唱的放完了，原曲小声伴着"
		return r, func() bool { o := a.lesson.takes.out; return o != nil && o.Playing() }
	case "play_vocals":
		n, ok := teacherLine(lines, c.Args, at)
		if !ok {
			return toolError(fmt.Sprintf("没有第 %v 句", c.Args["n"])), nil
		}
		a.stopRecording()
		a.stopTake()
		a.lesson.pointed = n + 1
		if !a.playVocals(n) {
			return toolError(a.vocals.err), nil
		}
		r := lineFor(lines, n)
		r["played"] = "原唱的人声放完了，已暂停"
		// Separating can wait on the song's download: the lesson does not
		// wait on it for ever.
		until := time.Now().Add(vocalsWait)
		return r, func() bool {
			if a.vocalsPlaying() && time.Now().Before(until) {
				return true
			}
			if why := a.vocals.err; a.vocals.want != 0 || why != "" {
				a.stopVocals()
				delete(r, "played")
				r["error"] = cmp.Or(why, "人声分离太慢，没放出来") + "：改用 play_line 放原曲"
			}
			return false
		}
	}
	return toolError("没有这个工具：" + c.Name), nil
}

// lessonAt is the line the lesson is at: the one the teacher points to,
// else the one chosen, else the one sung now, or the first sung after it
// for one of the credits, as at the song's start.
func (a *app) lessonAt(lines []lyrics.Line) int {
	if p := a.lesson.pointed; p > 0 && p <= len(lines) {
		return p - 1
	}
	if n, _ := a.pl.Line(); n >= 0 {
		return n
	}
	n := max(0, lyrics.Index(lines, a.pl.Position()))
	if n < len(lines) && lyrics.IsCredit(lines[n].Text) {
		n, _ = stepLine(lines, n, 1)
	}
	return n
}

// teacherLine is the line the teacher's args name, numbered as the lesson
// shows them, else at, reporting false for no such line or one of the
// credits, which are not sung.
func teacherLine(lines []lyrics.Line, args map[string]any, at int) (int, bool) {
	if v, ok := args["n"].(float64); ok {
		return numberedLine(lines, int(v))
	}
	return at, at >= 0 && at < len(lines) && !lyrics.IsCredit(lines[at].Text)
}

// stepLine is the line sung next after at, step 1, or before it, step -1.
func stepLine(lines []lyrics.Line, at, step int) (int, bool) {
	for n := at + step; n >= 0 && n < len(lines); n += step {
		if !lyrics.IsCredit(lines[n].Text) {
			return n, true
		}
	}
	return at, false
}

// lineFor is line n for the teacher: its number, words, romaji and
// translation.
func lineFor(lines []lyrics.Line, n int) map[string]any {
	r := map[string]any{"n": lineNumber(lines, n), "text": lines[n].Text}
	if ro := lines[n].Romaji; ro != "" {
		r["romaji"] = ro
	}
	if tl := lines[n].Translation; tl != "" {
		r["translation"] = tl
	}
	return r
}

// lyricsForTeacher is the song playing and its lines, for the teacher.
func (a *app) lyricsForTeacher() map[string]any {
	tr := a.pl.Current()
	lines, _ := a.pl.Lyrics()
	if tr == nil || len(lines) == 0 {
		return toolError("现在没有歌词：请学生先放一首有歌词的歌")
	}
	var sung []map[string]any
	for n, l := range lines {
		if !lyrics.IsCredit(l.Text) {
			sung = append(sung, lineFor(lines, n))
		}
	}
	return map[string]any{"song": tr.Name, "artists": tr.ArtistNames(), "lines": sung}
}

// teacherInstructions tells the teacher how to teach, and the song.
func (a *app) teacherInstructions() string {
	var b strings.Builder
	b.WriteString(teacherPrompt)
	b.WriteString(a.teacherLanguage())
	if a.vocalsReady() {
		b.WriteString(vocalsPrompt)
	}
	tr := a.pl.Current()
	lines, _ := a.pl.Lyrics()
	if tr == nil || len(lines) == 0 {
		b.WriteString("\n\n现在没有在放有歌词的歌：请学生先在播放器里放一首。")
		return b.String()
	}
	fmt.Fprintf(&b, "\n\n现在的歌：《%s》，%s。歌词：\n", tr.Name, tr.ArtistNames())
	for n, l := range lines {
		if lyrics.IsCredit(l.Text) {
			continue
		}
		fmt.Fprintf(&b, "%d. %s", lineNumber(lines, n), l.Text)
		if l.Romaji != "" {
			fmt.Fprintf(&b, " [%s]", l.Romaji)
		}
		if l.Translation != "" {
			fmt.Fprintf(&b, "（%s）", l.Translation)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// teacherLanguage is what language the teacher speaks in, as the
// listener chose.
func (a *app) teacherLanguage() string {
	if a.settings.TeacherEnglish {
		return "\n\nLanguage: always speak to the student in natural, friendly English, even though these instructions and the translations are in Chinese, and even if the student speaks Chinese. Quote the lyrics in their own language, with romaji when it helps. Keep each sentence under 15 words."
	}
	return "\n\n说话的语言：始终用简体中文和学生说话，不管学生说的、唱的是日语、英语还是别的语言；歌词本身照原文念。每句不超过 20 个字。"
}

// teacherPrompt is how the teacher teaches.
const teacherPrompt = `你是一位耐心、温暖的声乐老师，在一个音乐播放器里教学生唱歌，一次教一句。你自己不唱：原曲就是示范，用工具放给学生听。

像真人老师一样自然地说话：每次只说一句短话，不寒暄、不客套、不念学生屏幕上看得到的东西。换着说法，不要重复你上一次说过的话，也不要每次都问同样的问题。开场只说一句就开始放。

一句的教法：
1. 用 play_line 把这一句放给学生听。它播完才返回，返回前不要说话。
2. play_line 返回后，什么都不说，直接调用 start_recording（学生屏幕上会显示"跟着唱 · 老师在听"），然后安静地听，等学生唱完再说话。
3. 学生唱完后，由你决定下一步，不用每次都问学生：
   - 唱得不错：具体夸一下好在哪，然后直接 next_line、play_line 教下一句。
   - 有明显问题：只说最要紧的一个问题和一个具体做法，比如"「つづけ」的 tsu 太轻，舌尖顶住上齿再发"，然后直接 play_line 再放这一句让学生再唱。
   - 同一句已经练了三遍左右：鼓励一下，进下一句。
   只有拿不准时才偶尔问学生想再练还是下一句，而且每次换个问法。
4. 学生说要再来、下一句、放慢，就照做。

学生也可以随时提要求：放慢（slow_down）、听整首（play_song）、听自己唱的（play_recording）。歌词的行号从 1 开始，和学生屏幕上的编号一样。日语歌词可以用假名或罗马音帮学生读准；歌词后方括号里的是网易云给的这句的罗马音，有就照它的读法纠正发音。歌在放的时候你听不到学生说话。

说要放、要录、要换下一句，就在同一次回复里调用 play_line、loop_line、start_recording 或 next_line，不要只说不做。
你不会唱歌，不要试着唱：要听唱法，就用 play_line 放原曲，难的句子先用 slow_down 放慢到 0.75。学生要你示范发音时，用日语一个音一个音、慢慢地念，每个音之间停一下，比如「お、も、い、つ、づ、け、て」。`

// teacherTools are the tools the teacher runs the lesson with.
var teacherTools = []teacher.Tool{
	{Name: "get_lyrics", Description: "取现在这首歌的全部歌词，带行号和翻译。换了歌或不确定时用。"},
	{Name: "play_line", Description: "把第 n 句放给学生听（从这一句前一点点开始），放完自动暂停，放完才返回。不给 n 就放现在这一句。", Params: lineParams(false)},
	{Name: "loop_line", Description: "把第 n 句连着放 times 遍（1 到 5），放完暂停才返回。", Params: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n":     lineParam,
			"times": map[string]any{"type": "integer", "description": "放几遍，1 到 5"},
		},
	}},
	{Name: "play_song", Description: "从现在的位置放整首歌，直到学生暂停。"},
	{Name: "pause", Description: "暂停播放。"},
	{Name: "slow_down", Description: "设教唱的速度：1 是原速，0.9 或 0.75 更慢，音调不变。之后放的句子都用这个速度。", Params: map[string]any{
		"type":       "object",
		"properties": map[string]any{"rate": map[string]any{"type": "number", "description": "速度，0.5 到 1"}},
		"required":   []string{"rate"},
	}},
	{Name: "highlight_line", Description: "把歌词移到第 n 句并标出来，不放。", Params: lineParams(true)},
	{Name: "next_line", Description: "移到下一句，返回它的歌词，不放。"},
	{Name: "previous_line", Description: "移到上一句，返回它的歌词，不放。"},
	{Name: "start_recording", Description: "开始录学生清唱（没有伴奏）现在这一句（或第 n 句），返回这一句的歌词。调用后不要说话，安静地听；学生唱完停下，录音自动结束。", Params: lineParams(false)},
	{Name: "stop_recording", Description: "停止录音。"},
	{Name: "play_recording", Description: "把学生唱的这一句（或第 n 句）放给他听，原曲小声伴着，放完才返回。", Params: lineParams(false)},
	{Name: "play_vocals", Description: "只放第 n 句（不给 n 就是现在这一句）原唱歌手的人声，去掉伴奏，按 slow_down 的速度、原调，当作你的示范唱给学生听；分离和放完才返回。学生没下载人声分离模型时会返回错误，请他在设置的「跟唱」里下载。", Params: lineParams(false)},
}

// lineParam is a line's number, as a tool's parameter.
var lineParam = map[string]any{"type": "integer", "description": "歌词的行号，从 1 开始"}

// lineParams are the parameters of a tool taking a line, n, required or
// else the line the lesson is at.
func lineParams(required bool) map[string]any {
	p := map[string]any{"type": "object", "properties": map[string]any{"n": lineParam}}
	if required {
		p["required"] = []string{"n"}
	}
	return p
}
