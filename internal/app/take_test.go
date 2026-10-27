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
	m := &fakeMic{}
	old := startMic
	startMic = func(got func([]float32)) (interface{ Stop() }, error) {
		m.got = got
		return m, nil
	}
	t.Cleanup(func() { startMic = old })
	a := newTestApp()
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
	return a, tt, m
}

// Recording pauses the music; stopping keeps the take, of that line, and
// plays it back.
func TestRecordTake(t *testing.T) {
	a, tt, m := takeApp(t)
	if err := tt.Click("录我唱的"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pl.Playing() {
		t.Error("the music plays on while recording")
	}
	m.sing(1500 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("停止录音"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tk := a.lesson.takes
	if !m.stopped {
		t.Error("the microphone records on")
	}
	if got := len(tk.sung[1]); got != mic.Rate*3/2 {
		t.Errorf("kept %d samples of line 1", got)
	}
	if tk.out == nil || tk.line != 1 {
		t.Error("the take did not play back")
	}
	if !tt.HasText("听我唱的 1.5 秒") && !tt.HasText("停止") {
		t.Error("no button to hear the take")
	}
}

// A take stops by itself once as long as the line, and a little more.
func TestTakeStopsAtLimit(t *testing.T) {
	a, tt, m := takeApp(t)
	tt.Click("录我唱的")
	tt.Frame()
	limit := takeLimit(lyrics.Line{At: 13 * time.Second, End: 17 * time.Second}, 1)
	m.sing(limit + time.Second)
	tt.Frame()
	if a.lesson.takes.rec != nil || !m.stopped {
		t.Fatal("still recording past the limit")
	}
	if got := len(a.lesson.takes.sung[1]); got < int(limit.Seconds()*mic.Rate) {
		t.Errorf("kept %d samples, want the limit's", got)
	}
}

// A take too short to be singing is not kept.
func TestTakeTooShort(t *testing.T) {
	a, tt, m := takeApp(t)
	tt.Click("录我唱的")
	tt.Frame()
	m.sing(100 * time.Millisecond)
	tt.Click("停止录音")
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
	tt.Click("录我唱的")
	tt.Frame()
	if a.lesson.takes.rec != nil {
		t.Error("recording without a microphone")
	}
	if !tt.HasText("麦克风打不开：mic: no microphone") {
		t.Error("did not say the microphone failed")
	}
}
