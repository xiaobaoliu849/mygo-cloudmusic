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
	// through plays the song on from the line, for the listener to sing
	// along with it, without pausing at each line's end.
	through bool
	// under plays the song from from to to under a take of the listener
	// singing along with it, at the tempo they sang it at, then pauses.
	under    bool
	from, to time.Duration
	tempo    float64
	left     int // how many more times it plays after this one; -1 forever
	watch    int // counts the waits for the line's end, to drop stale ones
}

// Tempos a lesson can play its lines at, slower than the song, at its
// pitch.
const (
	TempoSlow   = 0.75
	TempoSlower = 0.9
)

// LineLead is how long a line plays before it starts.
const LineLead = lineLead

// underVolume is how loud the song plays under a take: softly, so the
// listener hears themselves over it, and what they sang along with even
// if they sang without it.
const underVolume = 0.25

// lineLead is how long before a line it starts playing: enough for the
// music to fade in, and for the listener to hear where the line comes in.
const lineLead = 400 * time.Millisecond

// lineTail lets a line's last word ring out before it pauses.
const lineTail = 150 * time.Millisecond

// Line returns the line of the lyrics the lesson is on, -1 for none, and
// whether it plays alone, to pause or loop at its end.
func (p *Player) Line() (int, bool) { return p.lesson.line, p.lesson.alone }

// SingingThrough reports whether the song plays on from a line for the
// listener to sing along with it, line after line.
func (p *Player) SingingThrough() bool { return p.lesson.through }

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
// line playing alone starting again at it. The song under a take plays
// on at the take's.
func (p *Player) SetTempo(t float64) {
	p.tempo = min(max(t, 0.5), 1)
	if p.lesson.alone && !p.lesson.under {
		p.fromLine()
	}
}

// speed is how fast the lesson plays: under a take, at the tempo it was
// sung at; else at the tempo.
func (p *Player) speed() float64 {
	if p.lesson.under {
		return p.lesson.tempo
	}
	return p.Tempo()
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
	if p.lesson.alone || p.lesson.through {
		t = p.speed()
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

// records reports whether the listener records themselves singing along.
func (p *Player) records() bool { return p.Records != nil && p.Records() }

// coached reports whether a teacher runs the lesson.
func (p *Player) coached() bool { return p.Coached != nil && p.Coached() }

// setAlong says whether the line plays for the listener to sing along,
// telling SingAlong of a change.
func (p *Player) setAlong(on bool) {
	if p.lesson.along == on {
		return
	}
	p.lesson.along = on
	if p.SingAlong != nil {
		p.SingAlong(on)
	}
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
	left := times - 1
	if times <= 0 {
		left = -1
	}
	return p.startLine(n, left, false)
}

// SingLine plays line n of the lyrics alone, softly, for the listener to
// sing along with it straight away, as to record them, then pauses at
// its end. It reports false, doing nothing, while the track or its lyrics
// load, or with no line n.
func (p *Player) SingLine(n int) bool { return p.startLine(n, 0, true) }

// SingThrough plays the song on from line n, softly as Guide says and
// at the tempo, for the listener to sing along with it, line after line,
// until StopLine, a seek, or the song's end, where it pauses. It reports
// false, doing nothing, while the track or its lyrics load, or with no
// line n.
func (p *Player) SingThrough(n int) bool {
	if p.out == nil || n < 0 || n >= len(p.lyrics) {
		return false
	}
	p.setAlong(false)
	p.lesson = lesson{line: n, through: true, watch: p.lesson.watch + 1}
	p.setAlong(true)
	p.applyGain()
	p.fromLine()
	return true
}

// PlayUnder plays the song from from, at tempo and softly, at
// underVolume, for d, then pauses: to play under a take of the listener
// singing along with it, d long, from where they sang, as fast as they
// heard it, whatever the lesson's tempo is now. It reports false, doing
// nothing, while the track loads.
func (p *Player) PlayUnder(from, d time.Duration, tempo float64) bool {
	if p.out == nil {
		return false
	}
	p.setAlong(false)
	from = max(0, from)
	if tempo == 0 {
		tempo = 1
	}
	tempo = min(max(tempo, 0.5), 1)
	to := from + time.Duration(float64(d)*tempo)
	p.lesson = lesson{line: p.lesson.line, alone: true, under: true, from: from, to: to, tempo: tempo, watch: p.lesson.watch + 1}
	p.applyGain()
	p.fromLine()
	return true
}

// PlayingUnder reports whether the song plays under a take.
func (p *Player) PlayingUnder() bool { return p.lesson.under }

// startLine plays line n alone, left more times after this one, to sing
// along with it or to hear it first.
func (p *Player) startLine(n, left int, along bool) bool {
	if p.out == nil || n < 0 || n >= len(p.lyrics) {
		return false
	}
	p.setAlong(false)
	p.lesson = lesson{line: n, alone: true, left: left, watch: p.lesson.watch + 1}
	p.setAlong(along)
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
	p.setAlong(false)
	p.lesson = lesson{line: -1, watch: p.lesson.watch + 1}
	p.applyGain()
}

// span returns where the lesson's line plays from and pauses.
func (p *Player) span() (from, to time.Duration) {
	if p.lesson.under {
		return p.lesson.from, p.lesson.to
	}
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
	wait := max(time.Duration(float64(to-p.Position())/p.speed()), 20*time.Millisecond)
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
// heard, if Guide or Records says so, and else if it has times left.
// Otherwise it pauses, the lesson staying on it.
func (p *Player) lineEnded() {
	if p.lesson.under {
		p.StopLine()
		return
	}
	if !p.lesson.along && !p.coached() && (p.guide() > 0 || p.records()) {
		p.setAlong(true)
		p.applyGain()
		p.fromLine()
		return
	}
	if p.lesson.left != 0 {
		if p.lesson.left > 0 {
			p.lesson.left--
		}
		p.setAlong(false)
		p.applyGain()
		p.fromLine()
		return
	}
	p.StopLine()
}

// StopLine pauses the line playing alone, as at its end, or the song
// sung through, the lesson staying on the line.
func (p *Player) StopLine() {
	if !p.lesson.alone && !p.lesson.through {
		return
	}
	// Loud again once it plays again, not as it fades out.
	p.lesson.alone, p.lesson.through, p.lesson.under = false, false, false
	if p.playing {
		p.TogglePlay()
	}
	p.setAlong(false)
}
