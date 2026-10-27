package app

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
)

// startMic records from the microphone, giving got what it hears.
var startMic = func(got func([]float32)) (interface{ Stop() }, error) {
	r, err := mic.Start(got)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// takes are the lines the listener sang in a lesson, recorded, for them
// to hear how they sang: one take a line, the last.
type takes struct {
	sung map[int][]float32 // by line, mono at mic.Rate
	want bool              // the listener asked to record the line sung along next
	rec  *recording
	out  *audio.Player // a take playing back
	line int           // the line of the take playing back
	err  string        // why the last take went wrong, to show
}

// A recording is a take on its way: the microphone gives it samples on
// a goroutine of its own.
type recording struct {
	line  int
	limit int // samples to stop at
	stop  interface{ Stop() }

	mu      sync.Mutex
	samples []float32
	level   float32 // how loud the last of it was, for the meter
	full    bool
}

// takeLimit is how long a take of line l may go on: as long as the line
// plays at tempo, and a little, for coming in late.
func takeLimit(l lyrics.Line, tempo float64) time.Duration {
	d := time.Duration(float64(l.End-l.At)/tempo) + 2*time.Second
	return min(max(d, 4*time.Second), 30*time.Second)
}

// hookLesson has the player ask the settings how a lesson goes, and tell
// the app as a line plays for the listener to sing along with it: to
// record them as they do, and play them back once it ends.
func (a *app) hookLesson() {
	a.pl.Guide = func() float64 { return a.settings.Guide }
	a.pl.Records = func() bool { return a.settings.AutoRecord }
	a.pl.SingAlong = func(on bool) {
		t := &a.lesson.takes
		if !on {
			a.stopRecording()
			return
		}
		want := t.want
		t.want = false
		n, _ := a.pl.Line()
		lines, _ := a.pl.Lyrics()
		if (want || a.settings.AutoRecord) && n >= 0 && n < len(lines) {
			a.record(n, takeLimit(lines[n], a.pl.Tempo()))
		}
	}
}

// singTake plays line n softly for the listener to sing along with it,
// recording them.
func (a *app) singTake(n int) {
	t := &a.lesson.takes
	a.stopTake()
	t.want = true
	if !a.pl.SingLine(n) {
		t.want = false
	}
}

// record starts recording the listener singing line n, for at most limit,
// with no take playing: the music plays on, for them to sing along.
func (a *app) record(n int, limit time.Duration) {
	t := &a.lesson.takes
	a.stopTake()
	a.stopRecording()
	rec := &recording{line: n, limit: int(limit.Seconds() * mic.Rate)}
	stop, err := startMic(func(s []float32) {
		rec.mu.Lock()
		if rec.full {
			rec.mu.Unlock()
			return
		}
		rec.samples = append(rec.samples, s...)
		rec.level = mic.Level(s)
		rec.full = len(rec.samples) >= rec.limit
		full := rec.full
		rec.mu.Unlock()
		if full {
			a.post(func() {
				if t.rec == rec {
					a.stopRecording()
				}
			})
		} else {
			a.post(func() {}) // the meter moves
		}
	})
	if err != nil {
		t.err = "麦克风打不开：" + err.Error()
		return
	}
	rec.stop = stop
	t.rec, t.err = rec, ""
}

// stopRecording ends the take on its way, keeping it unless too short to
// be singing, then plays it back, unless the music plays on.
func (a *app) stopRecording() {
	t := &a.lesson.takes
	rec := t.rec
	if rec == nil {
		return
	}
	t.rec = nil
	rec.stop.Stop()
	rec.mu.Lock()
	samples := rec.samples
	rec.mu.Unlock()
	if len(samples) < mic.Rate*3/10 {
		t.err = "录得太短了，再试一次"
		return
	}
	if mic.Level(samples) == 0 {
		t.err = "没录到声音：看看系统的声音设置里，默认的麦克风是否正确、没有静音"
	}
	if t.sung == nil {
		t.sung = map[int][]float32{}
	}
	t.sung[rec.line] = samples
	if !a.pl.Playing() {
		a.playTake(rec.line)
	}
}

// playTake plays back the take of line n, louder if sung quietly, with
// the music paused.
func (a *app) playTake(n int) {
	t := &a.lesson.takes
	samples, ok := t.sung[n]
	if !ok {
		return
	}
	a.stopTake()
	if a.pl.Playing() {
		a.pl.TogglePlay()
	}
	t.out, t.line = audio.NewSound(mic.Rate, 1, mic.Normalize(samples)).Play(), n
}

// stopTake stops a take playing back.
func (a *app) stopTake() {
	t := &a.lesson.takes
	if t.out != nil {
		t.out.Close()
		t.out = nil
	}
}

// forgetTakes drops the takes, as another song shows: they were of its
// lines.
func (a *app) forgetTakes() {
	a.stopRecording()
	a.stopTake()
	a.lesson.takes = takes{}
}

// takeBar records the listener singing line n, and plays it back: a
// meter shows the microphone hearing them as they sing.
func (a *app) takeBar(c *ui.Context, n int) {
	t := &a.lesson.takes
	rec := t.rec
	if rec != nil && rec.line != n {
		// Another line was chosen: that take is over.
		a.stopRecording()
		rec = nil
	}
	if t.out != nil && !t.out.Playing() {
		t.out.Close()
		t.out = nil
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(4, 0, 0, 8).Children(func() {
		if rec != nil {
			rec.mu.Lock()
			level, got, full := rec.level, len(rec.samples), rec.full
			rec.mu.Unlock()
			if full {
				a.stopRecording()
			}
			stop := pillButton(c.Key("stop-take"), nil, "停止录音", ui.Hex("#e5484d"), white).Label("停止录音")
			if stop.Clicked() {
				// Pausing the line sung along stops the take, which plays
				// back.
				if a.pl.SingingAlong() {
					a.pl.StopLine()
				} else {
					a.stopRecording()
				}
			}
			// The meter: how loud the microphone hears the listener.
			ui.Box(c).Width(120).Height(6).Radius(3).Background(white.Alpha(0.15)).Children(func() {
				ui.Box(c).Width(120 * level).Height(6).Radius(3).Background(ui.Hex("#46a758"))
			})
			ui.Text(c, fmt.Sprintf("%.1f 秒", float64(got)/mic.Rate)).FontSize(12.5).FontFeatures("tnum").TextColor(white.Alpha(0.6))
			c.After(50 * time.Millisecond)
			return
		}
		start := pillButton(c.Key("record"), iconPodcast, "录我唱的", white.Alpha(0.14), white).Label("录我唱的")
		if start.Clicked() {
			a.singTake(n)
		}
		if samples, ok := t.sung[n]; ok {
			playing := t.out != nil && t.line == n
			label := fmt.Sprintf("听我唱的 %.1f 秒", float64(len(samples))/mic.Rate)
			if playing {
				label = "停止"
			}
			b := pillButton(c.Key("play-take"), iconPlay, label, white.Alpha(0.14), white).Label("听我唱的")
			if b.Clicked() {
				if playing {
					a.stopTake()
				} else {
					a.playTake(n)
				}
			}
			if playing {
				c.After(100 * time.Millisecond)
			}
		}
		if t.err != "" {
			ui.Text(c, t.err).FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#f5a524")).Shrink(1).MinWidth(0)
		}
	})
}
