package player

import (
	"testing"
	"time"

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
