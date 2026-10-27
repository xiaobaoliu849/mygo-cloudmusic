package player

import (
	"time"

	"netease-music-mygo/internal/lyrics"
)

// A lesson plays a song's lyrics a line at a time, for the listener to
// sing each one after it: a line plays from a little before it starts,
// then pauses at its end, or plays again as many times as asked.
type lesson struct {
	line  int  // the line chosen, -1 for none
	alone bool // the line plays alone: it pauses or loops at its end
	along bool // it plays again, softly, for the listener to sing along
	left  int  // how many more times it plays after this one; -1 forever
	watch int  // counts the waits for the line's end, to drop stale ones
}

// Tempos a lesson can play its lines at, slower than the song, at its
// pitch.
const (
	TempoSlow   = 0.75
	TempoSlower = 0.9
)

// lineLead is how long before a line it starts playing: enough for the
// music to fade in, and for the listener to hear where the line comes in.
const lineLead = 400 * time.Millisecond

// lineTail lets a line's last word ring out before it pauses.
const lineTail = 150 * time.Millisecond

// Line returns the line of the lyrics the lesson is on, -1 for none, and
// whether it plays alone, to pause or loop at its end.
func (p *Player) Line() (int, bool) { return p.lesson.line, p.lesson.alone }

// SingingAlong reports whether the lesson's line plays again, softly, for
// the listener to sing along with it.
func (p *Player) SingingAlong() bool { return p.lesson.along }

// Tempo returns how fast a lesson plays its lines: 1 as the song goes, or
// slower, at the song's pitch.
func (p *Player) Tempo() float64 {
	if p.tempo == 0 {
		return 1
	}
	return p.tempo
}

// SetTempo sets how fast a lesson plays its lines, from 0.5 to 1, the
// line playing alone starting again at it.
func (p *Player) SetTempo(t float64) {
	p.tempo = min(max(t, 0.5), 1)
	if p.lesson.alone {
		p.fromLine()
	}
}

// applyTempo sets the speed of what plays: a lesson's tempo while its
// line plays alone, else the song's own. Out slows at once, and lowers
// the pitch, while the shifter raises it from the next seek, which the
// caller makes, reported true if needed.
func (p *Player) applyTempo() bool {
	if p.out == nil || p.shift == nil {
		return false
	}
	t := 1.0
	if p.lesson.alone {
		t = p.Tempo()
	}
	if p.out.Speed() == t {
		return false
	}
	p.out.SetSpeed(t)
	p.shift.setRatio(1 / t)
	return true
}

// guide is how loud the line plays as the listener sings along.
func (p *Player) guide() float64 {
	if p.Guide == nil {
		return 0
	}
	return min(max(p.Guide(), 0), 1)
}

// GuideChanged hears that the guide's volume changed, to play the line
// sung along with at it.
func (p *Player) GuideChanged() { p.applyGain() }

// PlayLine plays line n of the lyrics alone, then again, softly, for the
// listener to sing along, if Guide says so, then pauses at its end. It
// reports false, doing nothing, while the track or its lyrics load, or
// with no line n.
func (p *Player) PlayLine(n int) bool { return p.LoopLine(n, 1) }

// LoopLine plays line n of the lyrics times times over, each time once
// to hear and once softly to sing along, as PlayLine does, then pauses at
// its end; with times 0 or less, until something else plays. It reports
// false, doing nothing, while the track or its lyrics load, or with no
// line n.
func (p *Player) LoopLine(n, times int) bool {
	if p.out == nil || n < 0 || n >= len(p.lyrics) {
		return false
	}
	left := times - 1
	if times <= 0 {
		left = -1
	}
	p.lesson = lesson{line: n, alone: true, left: left, watch: p.lesson.watch + 1}
	p.applyGain()
	p.fromLine()
	return true
}

// NextLine plays the line after the lesson's, or after the one sung now.
func (p *Player) NextLine() bool { return p.PlayLine(p.lessonLine() + 1) }

// PrevLine plays the line before the lesson's, or before the one sung
// now.
func (p *Player) PrevLine() bool { return p.PlayLine(max(0, p.lessonLine()-1)) }

// lessonLine is the lesson's line, else the one sung at the position.
func (p *Player) lessonLine() int {
	if p.lesson.line >= 0 {
		return p.lesson.line
	}
	return lyrics.Index(p.lyrics, p.Position())
}

// EndLesson lets the song play on through its lines, as it would without
// a lesson.
func (p *Player) EndLesson() {
	p.lesson = lesson{line: -1, watch: p.lesson.watch + 1}
	p.applyGain()
}

// span returns where the lesson's line plays from and pauses.
func (p *Player) span() (from, to time.Duration) {
	l := p.lyrics[p.lesson.line]
	return max(0, l.At-lineLead), l.End + lineTail
}

// fromLine plays the lesson's line from its start, and waits for its end.
func (p *Player) fromLine() {
	from, _ := p.span()
	p.applyTempo()
	p.seek(from)
	switch {
	case !p.playing:
		p.TogglePlay()
	case !p.seeking && !p.out.Playing():
		// At the track's end: the seek landed at once, paused.
		p.out.Play()
	}
	p.watchLine()
}

// watchLine waits for the end of the line playing alone, checking it
// then: the position is where the listener hears, which seeks and fades
// move off the clock.
func (p *Player) watchLine() {
	if !p.lesson.alone || !p.playing {
		return
	}
	p.lesson.watch++
	watch := p.lesson.watch
	_, to := p.span()
	// The position goes at the speed: slower, it takes longer.
	wait := max(time.Duration(float64(to-p.Position())/p.Tempo()), 20*time.Millisecond)
	time.AfterFunc(wait, func() {
		p.post(func() {
			if watch == p.lesson.watch {
				p.checkLine()
			}
		})
	})
}

// checkLine pauses or loops the line once its end has played, and else
// waits on.
func (p *Player) checkLine() {
	if !p.lesson.alone || !p.playing || p.out == nil {
		return
	}
	if _, to := p.span(); p.Position() < to {
		p.watchLine()
		return
	}
	p.lineEnded()
}

// lineEnded plays the lesson's line again: softly to sing along, once
// heard, if Guide says so, and else if it has times left. Otherwise it
// pauses, the lesson staying on it.
func (p *Player) lineEnded() {
	if !p.lesson.along && p.guide() > 0 {
		p.lesson.along = true
		p.applyGain()
		p.fromLine()
		return
	}
	// Loud again: to hear the line once more, or, pausing, once it plays
	// again, not as it fades out.
	p.lesson.along = false
	if p.lesson.left != 0 {
		if p.lesson.left > 0 {
			p.lesson.left--
		}
		p.applyGain()
		p.fromLine()
		return
	}
	p.lesson.alone = false
	if p.playing {
		p.TogglePlay()
	}
}
