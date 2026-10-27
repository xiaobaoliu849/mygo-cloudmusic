package player

import (
	"io"
	"math"
	"sync"

	"github.com/egoist/mygo/audio"
)

// A shifter raises the pitch of a decoder's sound by a ratio, keeping its
// length, for a lesson to play a song slower at its own pitch: the audio
// player's speed slows the sound, and lowers it, by as much as the
// shifter raised it. Since the length stays, positions stay the song's,
// and the lyrics keep time.
//
// It stretches the sound by the ratio with WSOLA, laying windows of it
// half over each other, each taken from where it best continues the one
// before, then resamples the stretched sound back to its length. At a
// ratio of 1 it passes the sound through.
type shifter struct {
	src audio.Decoder
	ch  int

	mu   sync.Mutex
	next float64 // the ratio to shift by from the next SetPosition

	ratio float64
	n     int // frames a window
	hop   int // frames between windows laid down: half of one
	reach int // how far either way a window is looked for
	win   []float32
	first []float32 // the first window: whole for its first half, as nothing came before

	// Frames are counted from where the shifter last moved to, its start.
	start int64
	in    []float32 // the source's frames from inAt, interleaved
	inAt  int64
	inEnd int64 // frames read from the source
	eof   bool
	y     []float32 // the stretched sound from yAt, laid down
	yAt   int64
	ready int64   // the stretched frames before it are whole
	k     int64   // the next window
	taken int64   // where the last window came from
	out   int64   // frames given
	buf   []float32
}

func newShifter(src audio.Decoder) *shifter {
	s := &shifter{src: src, ch: max(src.Channels(), 1), next: 1, ratio: 1}
	rate := src.SampleRate()
	s.n = int(float64(rate)*0.045) &^ 1
	s.hop = s.n / 2
	s.reach = rate * 12 / 1000
	s.win = make([]float32, s.n)
	s.first = make([]float32, s.n)
	for i := range s.win {
		s.win[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(s.n)))
		s.first[i] = s.win[i]
		if i < s.hop {
			s.first[i] = 1
		}
	}
	return s
}

func (s *shifter) SampleRate() int { return s.src.SampleRate() }
func (s *shifter) Channels() int   { return s.src.Channels() }
func (s *shifter) Len() int64      { return s.src.Len() }

// setRatio sets the ratio to raise the pitch by, from the next
// SetPosition: what was read ahead already plays as it was.
func (s *shifter) setRatio(r float64) {
	s.mu.Lock()
	s.next = r
	s.mu.Unlock()
}

func (s *shifter) SetPosition(frame int64) error {
	s.mu.Lock()
	s.ratio = s.next
	s.mu.Unlock()
	s.start = frame
	s.in, s.inAt, s.inEnd, s.eof = s.in[:0], 0, 0, false
	s.y, s.yAt, s.ready, s.k, s.taken, s.out = s.y[:0], 0, 0, 0, 0, 0
	return s.src.SetPosition(frame)
}

func (s *shifter) Read(dst []float32) (int, error) {
	if s.ratio == 1 {
		return s.src.Read(dst)
	}
	ch := s.ch
	frames := len(dst) / ch
	for f := 0; f < frames; f++ {
		// The output frame's place in the stretched sound, and the four
		// frames around it to interpolate between.
		m := float64(s.out) * s.ratio
		i := int64(m)
		for s.ready < i+3 && !s.ended() {
			if err := s.lay(); err != nil {
				return f * ch, err
			}
		}
		if s.eof && s.out >= s.inEnd {
			if f == 0 {
				return 0, io.EOF
			}
			return f * ch, nil
		}
		t := float32(m - float64(i))
		for c := range ch {
			dst[f*ch+c] = cubic(s.yAt0(i-1, c), s.yAt0(i, c), s.yAt0(i+1, c), s.yAt0(i+2, c), t)
		}
		s.out++
	}
	s.trim(int64(float64(s.out)*s.ratio) - 2)
	return frames * ch, nil
}

// ended reports whether the windows have gone past the source's end.
func (s *shifter) ended() bool {
	return s.eof && float64(s.k*int64(s.hop))/s.ratio >= float64(s.inEnd)
}

// lay lays down the next window of the stretch: from where it would fall
// at the ratio, give or take reach, where it best goes on from the last.
func (s *shifter) lay() error {
	hop := int64(s.hop)
	at := int64(math.Round(float64(s.k*hop) / s.ratio))
	from, w := int64(0), s.first
	if s.k > 0 {
		w = s.win
		if err := s.fill(at + int64(s.reach+s.n)); err != nil {
			return err
		}
		from = s.best(at, s.taken+hop)
	} else if err := s.fill(int64(s.n)); err != nil {
		return err
	}
	base := s.k * hop
	if need := int(base + int64(s.n) - s.yAt); need*s.ch > len(s.y) {
		s.y = append(s.y, make([]float32, need*s.ch-len(s.y))...)
	}
	for i := range s.n {
		for c := range s.ch {
			s.y[int(base-s.yAt+int64(i))*s.ch+c] += w[i] * s.x(from+int64(i), c)
		}
	}
	s.taken = from
	s.k++
	s.ready = s.k * hop
	if s.ended() {
		s.ready = math.MaxInt64
	}
	return nil
}

// best returns where, within reach of at, a window's first half sounds
// most like the frames from want, as the last window would have gone on:
// looked for coarsely, every few frames, then frame by frame around the
// best.
func (s *shifter) best(at, want int64) int64 {
	lo := max(at-int64(s.reach), 0)
	hi := at + int64(s.reach)
	score := func(p int64, stride int) float32 {
		var dot, energy float32
		for i := 0; i < s.hop; i += stride {
			a := s.mono(p + int64(i))
			dot += a * s.mono(want+int64(i))
			energy += a * a
		}
		if energy == 0 {
			return 0
		}
		return dot / float32(math.Sqrt(float64(energy)))
	}
	pick, top := at, float32(math.Inf(-1))
	for p := lo; p <= hi; p += 4 {
		if v := score(p, 4); v > top {
			pick, top = p, v
		}
	}
	coarse := pick
	top = float32(math.Inf(-1))
	for p := max(coarse-3, lo); p <= min(coarse+3, hi); p++ {
		if v := score(p, 2); v > top {
			pick, top = p, v
		}
	}
	return pick
}

// fill reads the source on to frame end, or its end.
func (s *shifter) fill(end int64) error {
	for s.inEnd < end && !s.eof {
		if cap(s.buf) == 0 {
			s.buf = make([]float32, 4096*s.ch)
		}
		n, err := s.src.Read(s.buf)
		s.in = append(s.in, s.buf[:n]...)
		s.inEnd += int64(n / s.ch)
		if err == io.EOF {
			s.eof = true
		} else if err != nil {
			return err
		}
	}
	return nil
}

// x returns channel c of source frame i, nothing outside what was read.
func (s *shifter) x(i int64, c int) float32 {
	if i < s.inAt || i >= s.inEnd {
		return 0
	}
	return s.in[int(i-s.inAt)*s.ch+c]
}

// mono returns source frame i, its channels together.
func (s *shifter) mono(i int64) float32 {
	if s.ch == 1 {
		return s.x(i, 0)
	}
	return s.x(i, 0) + s.x(i, 1)
}

// yAt0 returns channel c of stretched frame i, nothing outside it.
func (s *shifter) yAt0(i int64, c int) float32 {
	j := int(i-s.yAt)*s.ch + c
	if i < s.yAt || j >= len(s.y) {
		return 0
	}
	return s.y[j]
}

// trim drops the stretched frames before y, and the source frames no
// window will take again, once there are enough of them to be worth it.
func (s *shifter) trim(y int64) {
	if drop := y - s.yAt; drop > int64(4*s.n) {
		s.y = append(s.y[:0], s.y[int(drop)*s.ch:]...)
		s.yAt = y
	}
	keep := min(int64(math.Round(float64(s.k*int64(s.hop))/s.ratio))-int64(s.reach), s.taken+int64(s.hop)) - 1
	if drop := keep - s.inAt; drop > int64(4*s.n) {
		s.in = append(s.in[:0], s.in[int(drop)*s.ch:]...)
		s.inAt = keep
	}
}

// cubic interpolates between b and c, t of the way, by Catmull-Rom.
func cubic(a, b, c, d, t float32) float32 {
	return b + 0.5*t*(c-a+t*(2*a-5*b+4*c-d+t*(3*(b-c)+d-a)))
}
