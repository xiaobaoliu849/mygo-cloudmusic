package app

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
	"netease-music-mygo/internal/netease"
	"netease-music-mygo/internal/player"
)

// fakeMic stands in for the microphone: the test sings into it.
type fakeMic struct {
	got     func([]float32)
	stopped bool
}

func (m *fakeMic) Stop() { m.stopped = true }

// sing gives the microphone d of a voice.
func (m *fakeMic) sing(d time.Duration) {
	s := make([]float32, int(d.Seconds()*mic.Rate))
	for i := range s {
		s[i] = float32(0.2 * math.Sin(2*math.Pi*220*float64(i)/mic.Rate))
	}
	m.got(s)
}

// takeApp is a lesson on line 1 of a song, the pane showing, the
// microphone fake.
func takeApp(t *testing.T) (*app, *ui.Tester, *fakeMic) {
	t.Helper()
	return takeAppPosting(t, nil)
}

// takeAppPosting is takeApp, the player posting to post, if not nil, as
// to the window, rather than dropping what it posts.
func takeAppPosting(t *testing.T, post func(func())) (*app, *ui.Tester, *fakeMic) {
	t.Helper()
	m := &fakeMic{}
	old := startMic
	startMic = func(got func([]float32)) (interface{ Stop() }, error) {
		m.got = got
		return m, nil
	}
	t.Cleanup(func() { startMic = old })
	a := newTestApp()
	if post != nil {
		a.pl = player.New(post)
		a.pl.SetVolume(0.5)
		a.hookLesson()
	}
	a.settings = defaultSettings()
	a.pl.Cue([]netease.Track{{ID: 7, Name: "春日影", Duration: 200000}})
	a.pl.SetLyrics([]lyrics.Line{
		{At: 10 * time.Second, End: 13 * time.Second, Text: "悴んだ心ふるえる眼差し"},
		{At: 13 * time.Second, End: 17 * time.Second, Text: "世界で僕は独りぼっちだった"},
	})
	out := audio.NewPlayer(&silence{})
	out.SetVolume(0)
	out.Play()
	a.pl.Attach(out)
	t.Cleanup(out.Close)
	t.Cleanup(a.stopTake)
	a.nowPlaying = true
	a.npPane = paneLesson
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("世界で僕は独りぼっちだった"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// The line heard: it pauses at its end, for the listener to sing it.
	a.pl.StopLine()
	tt.Frame()
	return a, tt, m
}

// Recording plays the line softly to sing along with; stopping pauses
// it, keeps the take, of that line, and plays it back with the song
// under it.
func TestRecordTake(t *testing.T) {
	a, tt, m := takeApp(t)
	if err := tt.Click("开始唱"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.pl.Playing() || !a.pl.SingingAlong() || a.lesson.takes.rec == nil {
		t.Fatalf("playing %v, singing along %v, recording %v", a.pl.Playing(), a.pl.SingingAlong(), a.lesson.takes.rec != nil)
	}
	m.sing(1500 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("停止"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tk := a.lesson.takes
	if !m.stopped {
		t.Error("the microphone records on")
	}
	if got := len(tk.sung[1].samples); got != mic.Rate*3/2 {
		t.Errorf("kept %d samples of line 1", got)
	}
	// The song plays under the take, softly, from where it was sung along.
	if !a.pl.PlayingUnder() || a.pl.Position() != 13*time.Second-player.LineLead {
		t.Errorf("under the take %v, from %v", a.pl.PlayingUnder(), a.pl.Position())
	}
	if tk.out == nil || tk.key != 1 {
		t.Error("the take did not play back")
	}
	if err := tt.Click("听我唱的"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.lesson.takes.out != nil || a.pl.Playing() {
		t.Error("stopping the take left it, or the song under it, playing")
	}
	if _, ok := tt.Find("听我唱的"); !ok {
		t.Error("no button to hear the take")
	}
}

// A take stops by itself once as long as the line, and a little more.
func TestTakeStopsAtLimit(t *testing.T) {
	a, tt, m := takeApp(t)
	tt.Click("开始唱")
	tt.Frame()
	limit := takeLimit(lyrics.Line{At: 13 * time.Second, End: 17 * time.Second}, 1)
	m.sing(limit + time.Second)
	tt.Frame()
	if a.lesson.takes.rec != nil || !m.stopped {
		t.Fatal("still recording past the limit")
	}
	if got := len(a.lesson.takes.sung[1].samples); got < int(limit.Seconds()*mic.Rate) {
		t.Errorf("kept %d samples, want the limit's", got)
	}
}

// A take too short to be singing is not kept.
func TestTakeTooShort(t *testing.T) {
	a, tt, m := takeApp(t)
	tt.Click("开始唱")
	tt.Frame()
	m.sing(100 * time.Millisecond)
	tt.Click("停止")
	tt.Frame()
	if _, ok := a.lesson.takes.sung[1]; ok {
		t.Error("kept a take of a tenth of a second")
	}
	if !tt.HasText("录得太短了，再试一次") {
		t.Error("did not say why")
	}
}

// A microphone that cannot open says why, and records nothing.
func TestMicFails(t *testing.T) {
	a, tt, _ := takeApp(t)
	startMic = func(func([]float32)) (interface{ Stop() }, error) { return nil, errors.New("mic: no microphone") }
	tt.Click("开始唱")
	tt.Frame()
	if a.lesson.takes.rec != nil {
		t.Error("recording without a microphone")
	}
	if !tt.HasText("麦克风打不开：mic: no microphone") {
		t.Error("did not say the microphone failed")
	}
}

// With 跟唱时录音 on, the listener is recorded as the line plays again
// to sing along with, unasked; off, not.
func TestAutoRecord(t *testing.T) {
	a, tt, _ := takeApp(t)
	a.pl.SingLine(1) // as the line plays again, once heard
	tt.Frame()
	if a.lesson.takes.rec == nil {
		t.Fatal("not recording as the listener sings along")
	}
	tt.Click("跟唱设置")
	tt.Frame()
	if err := tt.Click("自动录音关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.pl.SingLine(0)
	tt.Frame()
	if a.lesson.takes.rec != nil {
		t.Error("recording with 跟唱时录音 off")
	}
}

// Sung through, a click on a line plays the song on from it, recording
// the listener; stopping pauses it, and plays the take of it all back.
func TestSingThroughTake(t *testing.T) {
	a, tt, m := takeApp(t)
	if err := tt.Click("学法整首"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("悴んだ心ふるえる眼差し"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.pl.SingingThrough() || a.lesson.takes.rec == nil {
		t.Fatalf("singing through %v, recording %v", a.pl.SingingThrough(), a.lesson.takes.rec != nil)
	}
	m.sing(2 * time.Second)
	if err := tt.Click("停止"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tk := a.lesson.takes
	if a.pl.SingingThrough() || !a.pl.PlayingUnder() {
		t.Error("the song sings on after stopping, or does not play under the take")
	}
	if len(tk.sung[throughTake].samples) != 2*mic.Rate || tk.out == nil || tk.key != throughTake {
		t.Errorf("kept %d samples, playing back %v", len(tk.sung[throughTake].samples), tk.out != nil)
	}
	// Back to a line at a time, the take of the song is not the line's.
	tt.Click("学法逐句")
	tt.Frame()
	if tk := a.lesson.takes; tk.out != nil {
		t.Error("the take plays on, changing how to learn")
	}
}

// The delay of the speakers and microphone moves where a take starts in
// the song; nudged by ear, it moves the takes there are too.
func TestTakeSync(t *testing.T) {
	a, tt, m := takeApp(t)
	a.settings.Sync = -0.35
	tt.Click("开始唱")
	tt.Frame()
	m.sing(time.Second)
	tt.Click("停止")
	tt.Frame()
	want := 13*time.Second - player.LineLead - 350*time.Millisecond
	if at := a.lesson.takes.sung[1].at; at != want {
		t.Errorf("take at %v, want %v", at, want)
	}
	tt.Click("跟唱设置")
	tt.Frame()
	if err := tt.Click("人声晚一点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if at := a.lesson.takes.sung[1].at; at != want+50*time.Millisecond {
		t.Errorf("nudged, take at %v, want %v", at, want+50*time.Millisecond)
	}
	if math.Abs(a.settings.Sync+0.30) > 1e-9 {
		t.Errorf("sync %v, want -0.30", a.settings.Sync)
	}
}

// A take keeps the tempo it was sung at, so the song plays under it at
// that tempo after the lesson's changes; takes used to play the song at
// the lesson's tempo of the moment, out of step with the voice.
func TestTakeKeepsTempo(t *testing.T) {
	a, tt, m := takeApp(t)
	a.pl.SetTempo(player.TempoSlow)
	tt.Click("开始唱")
	tt.Frame()
	m.sing(time.Second)
	tt.Click("停止")
	tt.Frame()
	tt.Click("听我唱的")
	tt.Frame()
	a.pl.SetTempo(1)
	if err := tt.Click("听我唱的"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tk := a.lesson.takes.sung[1]
	if tk == nil {
		t.Fatal("no take of line 1")
	}
	if tk.tempo != player.TempoSlow {
		t.Errorf("take kept tempo %v, want %v", tk.tempo, player.TempoSlow)
	}
	if !a.pl.PlayingUnder() || a.pl.Tempo() != 1 {
		t.Errorf("under the take %v, lesson tempo %v", a.pl.PlayingUnder(), a.pl.Tempo())
	}
}

// Played back, the take of the song sung through keeps the line the
// song under it is on in view, line after line; the pane used to stay
// on the line it was sung from, the lit line scrolling away.
func TestThroughTakeFollowsLines(t *testing.T) {
	// The player's posts, run here as the window would: its pauses and
	// seeks land through them.
	posts := make(chan func(), 1024)
	pump := func() {
		for {
			select {
			case fn := <-posts:
				fn()
			default:
				return
			}
		}
	}
	a, tt, m := takeAppPosting(t, func(fn func()) { posts <- fn })
	a.pl.SetLyrics([]lyrics.Line{
		{At: 200 * time.Millisecond, End: 600 * time.Millisecond, Text: "第一句"},
		{At: 600 * time.Millisecond, End: time.Second, Text: "第二句"},
		{At: time.Second, End: 1400 * time.Millisecond, Text: "第三句"},
		{At: 1400 * time.Millisecond, End: 1800 * time.Millisecond, Text: "第四句"},
	})
	tt.Frame()
	tt.Click("学法整首")
	tt.Frame()
	if err := tt.Click("第一句"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	m.sing(2 * time.Second)
	tt.Click("停止")
	tt.Frame()
	if !a.playingThroughTake() {
		t.Fatal("the take of the song sung through does not play back")
	}
	for deadline := time.Now().Add(3 * time.Second); a.lesson.at < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("at %v in the song, the pane stays on line %d", a.pl.Position(), a.lesson.at+1)
		}
		time.Sleep(20 * time.Millisecond)
		pump()
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
	}
}
