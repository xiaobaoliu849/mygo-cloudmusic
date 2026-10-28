package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/netease"
	"netease-music-mygo/internal/player"
	"netease-music-mygo/internal/teacher"
)

// A click on a line of the lesson pane plays that line alone; with the
// loop on, the next line chosen loops.
func TestLessonPanePlaysLine(t *testing.T) {
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
	defer out.Close()
	a.nowPlaying = true
	a.npPane = paneLesson
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("世界で僕は独りぼっちだった"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n, alone := a.pl.Line(); n != 1 || !alone {
		t.Errorf("line %d, alone %v; want line 1 alone", n, alone)
	}
	if err := tt.Click("单句循环"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.lesson.loop {
		t.Error("loop not on")
	}
	if err := tt.Click("上一句"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n, _ := a.pl.Line(); n != 0 {
		t.Errorf("after 上一句, line %d", n)
	}
}

// The lesson's settings choose whose model the teacher is, and keep it.
// With NM_FRAMES set, saves them open, in a window of a laptop's height.
func TestLessonTeacherProvider(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.pl.Cue([]netease.Track{{ID: 7, Name: "春日影", Duration: 200000}})
	a.pl.SetLyrics([]lyrics.Line{{At: 10 * time.Second, End: 13 * time.Second, Text: "悴んだ心ふるえる眼差し", Romaji: "ka ji ka n da"}})
	a.nowPlaying = true
	a.npPane = paneLesson
	tt := ui.NewTester(a.view, 1000, 650)
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("跟唱设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if dir := os.Getenv("NM_FRAMES"); dir != "" {
		f, _ := os.Create(filepath.Join(dir, "lesson-settings.png"))
		png.Encode(f, tt.Image())
		f.Close()
	}
	if err := tt.Click("AI 老师模型Gemini"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.TeacherProvider != teacher.Gemini {
		t.Errorf("chose Gemini, provider %q", a.settings.TeacherProvider)
	}
}

// The lesson's choices set how loud a line plays to sing along with,
// and how fast.
func TestLessonGuideChoice(t *testing.T) {
	a := newTestApp()
	a.settings = defaultSettings()
	a.pl.Cue([]netease.Track{{ID: 7, Name: "春日影", Duration: 200000}})
	a.pl.SetLyrics([]lyrics.Line{{At: 10 * time.Second, End: 13 * time.Second, Text: "悴んだ心ふるえる眼差し"}})
	a.nowPlaying = true
	a.npPane = paneLesson
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("跟唱设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, o := range []struct {
		label string
		v     float64
	}{{"原唱音量中等", guideMedium}, {"原唱音量不放", 0}} {
		if err := tt.Click(o.label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.settings.Guide != o.v {
			t.Errorf("after %s, guide %v", o.label, a.settings.Guide)
		}
	}
	tt.Frame()
	if err := tt.Click("速度0.75×"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pl.Tempo() != player.TempoSlow || a.settings.Tempo != player.TempoSlow {
		t.Errorf("after 0.75×, tempo %v, kept %v", a.pl.Tempo(), a.settings.Tempo)
	}
}
