package player

import (
	"io"
	"time"

	"github.com/egoist/mygo/audio"
)

// A held sound is sound in memory, as a line's singing separated from its
// music, to play as a lesson plays its lines.
type held struct {
	rate, ch int
	samples  []float32 // interleaved
	at       int       // the next sample
}

func (h *held) SampleRate() int { return h.rate }
func (h *held) Channels() int   { return h.ch }
func (h *held) Len() int64      { return int64(len(h.samples) / h.ch) }

func (h *held) SetPosition(frame int64) error {
	h.at = min(int(frame)*h.ch, len(h.samples))
	return nil
}

func (h *held) Read(dst []float32) (int, error) {
	n := copy(dst[:len(dst)/h.ch*h.ch], h.samples[h.at:])
	h.at += n
	if h.at >= len(h.samples) {
		return n, io.EOF
	}
	return n, nil
}

// PlayAtTempo plays samples, interleaved, of ch channels at rate, at
// tempo, from 0.5 to 1, at their own pitch, as a lesson slows its lines.
func PlayAtTempo(rate, ch int, samples []float32, tempo float64) *audio.Player {
	tempo = min(max(tempo, 0.5), 1)
	s := newShifter(&held{rate: rate, ch: max(ch, 1), samples: samples})
	s.setRatio(1 / tempo)
	s.SetPosition(0)
	out := audio.NewPlayer(s)
	out.SetSpeed(tempo)
	out.Play()
	return out
}

// LineSpan returns where line n plays from and pauses, as a lesson plays
// it alone, reporting false with no line n.
func (p *Player) LineSpan(n int) (from, to time.Duration, ok bool) {
	if n < 0 || n >= len(p.lyrics) {
		return 0, 0, false
	}
	l := p.lyrics[n]
	return max(0, l.At-lineLead), l.End + lineTail, true
}
