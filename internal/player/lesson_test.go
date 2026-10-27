package player

import (
	"testing"
	"time"

	"github.com/egoist/mygo/audio"

	"netease-music-mygo/internal/lyrics"
)

// lessonLines are three short lines, the second at 10 s.
var lessonLines = []lyrics.Line{
	{At: 5 * time.Second, End: 5300 * time.Millisecond, Text: "一"},
	{At: 10 * time.Second, End: 10300 * time.Millisecond, Text: "二"},
	{At: 20 * time.Second, End: 20300 * time.Millisecond, Text: "三"},
}

// until runs what the player posts until done reports true, failing after
// a few seconds.
func until(t *testing.T, ch chan func(), done func() bool) {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for !done() {
		select {
		case fn := <-ch:
			fn()
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}

// A line played alone starts a little before it and pauses once it ends.
func TestPlayLinePausesAtItsEnd(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	if !p.PlayLine(1) {
		t.Fatal("did not play the line")
	}
	if pos := p.Position(); pos != 10*time.Second-lineLead {
		t.Errorf("started at %v", pos)
	}
	until(t, ch, func() bool { return !p.Playing() })
	if pos := p.out.Position(); pos < 10300*time.Millisecond || pos > 11*time.Second {
		t.Errorf("paused at %v", pos)
	}
	if n, alone := p.Line(); n != 1 || alone {
		t.Errorf("after the line: line %d, alone %v", n, alone)
	}
}

// A line looped plays the times asked, from its start each time, then
// pauses.
func TestLoopLinePlaysTimesOver(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	start := time.Now()
	p.LoopLine(1, 2)
	until(t, ch, func() bool { return !p.Playing() })
	once := lineLead + 300*time.Millisecond + lineTail
	if took := time.Since(start); took < 2*once {
		t.Errorf("paused after %v: played the line once", took)
	}
	if pos := p.out.Position(); pos > 11*time.Second {
		t.Errorf("paused at %v", pos)
	}
}

// Seeking elsewhere ends the lesson: the song plays on past the line.
func TestSeekEndsLesson(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	p.PlayLine(1)
	p.Seek(30 * time.Second)
	if n, _ := p.Line(); n != -1 {
		t.Errorf("still on line %d", n)
	}
	until(t, ch, func() bool { return !p.seeking })
	time.Sleep(lineLead + 500*time.Millisecond)
	for len(ch) > 0 {
		next(t, ch)
	}
	if !p.Playing() || !p.out.Playing() {
		t.Error("paused at the line's end after seeking away")
	}
}

// The next line follows the lesson's, or, with none, the line sung now.
func TestNextLine(t *testing.T) {
	_, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	p.out.Seek(12 * time.Second)
	p.NextLine()
	if n, _ := p.Line(); n != 2 {
		t.Errorf("from the song at 12 s, the next line is %d", n)
	}
	p.PrevLine()
	p.PrevLine()
	p.PrevLine()
	if n, _ := p.Line(); n != 0 {
		t.Errorf("back past the first line: line %d", n)
	}
	if p.PlayLine(3) {
		t.Error("played a line past the last")
	}
}

// With a guide, a line plays once to hear, then again softly to sing
// along, then pauses; playing on is loud again.
func TestPlayLineSingsAlongSoftly(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	p.Guide = func() float64 { return 0.25 }
	gain := func() float64 {
		p.fade.mu.Lock()
		defer p.fade.mu.Unlock()
		return p.fade.gain
	}
	full := gain()
	p.PlayLine(1)
	until(t, ch, p.SingingAlong)
	if g := gain(); g != full*0.25 {
		t.Errorf("sang along at %v, want %v", g, full*0.25)
	}
	if !p.Playing() {
		t.Fatal("paused before singing along")
	}
	until(t, ch, func() bool { return !p.Playing() })
	if p.SingingAlong() {
		t.Error("still singing along, paused")
	}
	if pos := p.out.Position(); pos > 11*time.Second {
		t.Errorf("paused at %v", pos)
	}
	p.TogglePlay()
	if g := gain(); g != full {
		t.Errorf("played on at %v, want %v", g, full)
	}
}

// Slower, a line plays at the tempo, still pausing at its end, and the
// song plays on at its own speed after.
func TestPlayLineSlower(t *testing.T) {
	ch, p := posted()
	p.out.Close()
	p.shift = newShifter(silence{})
	out := audio.NewPlayer(p.shift)
	out.SetVolume(0)
	out.Play()
	p.Attach(out)
	p.fade.attach(out, 1)
	defer out.Close()
	p.SetLyrics(lessonLines)
	p.SetTempo(TempoSlow)
	start := time.Now()
	p.PlayLine(1)
	if s := p.out.Speed(); s != TempoSlow {
		t.Errorf("line at speed %v", s)
	}
	until(t, ch, func() bool { return !p.Playing() })
	once := lineLead + 300*time.Millisecond + lineTail
	if took := time.Since(start); took < time.Duration(float64(once)/TempoSlow)-50*time.Millisecond {
		t.Errorf("line took %v, not slower than %v", took, once)
	}
	if pos := p.out.Position(); pos < 10300*time.Millisecond || pos > 11*time.Second {
		t.Errorf("paused at %v", pos)
	}
	p.TogglePlay()
	if s := p.out.Speed(); s != 1 {
		t.Errorf("played on at speed %v", s)
	}
}

// Singing a line plays it softly straight away, telling SingAlong as it
// starts, and as it stops, the music paused by then.
func TestSingLineTellsSingAlong(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	p.Guide = func() float64 { return 0.25 }
	var heard []bool
	pausedAtStop := false
	p.SingAlong = func(on bool) {
		heard = append(heard, on)
		if !on {
			pausedAtStop = !p.Playing()
		}
	}
	p.SingLine(1)
	if !p.SingingAlong() || len(heard) != 1 || !heard[0] {
		t.Fatalf("singing along %v, heard %v", p.SingingAlong(), heard)
	}
	until(t, ch, func() bool { return !p.Playing() })
	if len(heard) != 2 || heard[1] || !pausedAtStop {
		t.Errorf("heard %v, paused at the stop %v", heard, pausedAtStop)
	}
}

// Recording with no guide, the line still plays again, silent, for as
// long as the listener sings it.
func TestRecordsWithoutGuide(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	p.Records = func() bool { return true }
	var heard []bool
	p.SingAlong = func(on bool) { heard = append(heard, on) }
	p.PlayLine(1)
	until(t, ch, p.SingingAlong)
	p.fade.mu.Lock()
	gain := p.fade.gain
	p.fade.mu.Unlock()
	if gain != 0 {
		t.Errorf("sang along at %v, want silence", gain)
	}
	until(t, ch, func() bool { return !p.Playing() })
	if len(heard) != 2 {
		t.Errorf("heard %v", heard)
	}
}

// Seeking away as the listener sings along stops the singing along.
func TestSeekStopsSingingAlong(t *testing.T) {
	_, p := posted()
	defer p.out.Close()
	p.SetLyrics(lessonLines)
	var heard []bool
	p.SingAlong = func(on bool) { heard = append(heard, on) }
	p.SingLine(1)
	p.Seek(30 * time.Second)
	if p.SingingAlong() || len(heard) != 2 || heard[1] {
		t.Errorf("singing along %v, heard %v", p.SingingAlong(), heard)
	}
}
