package player

import (
	"io"
	"math"
	"testing"
)

// tone is a few seconds of a sine at hz, in stereo.
type tone struct {
	hz  float64
	len int64
	at  int64
}

func (t *tone) SampleRate() int { return 48000 }
func (t *tone) Channels() int   { return 2 }
func (t *tone) Len() int64      { return t.len }
func (t *tone) SetPosition(f int64) error {
	t.at = f
	return nil
}
func (t *tone) Read(b []float32) (int, error) {
	n := 0
	for ; n+1 < len(b) && t.at < t.len; n += 2 {
		v := float32(0.5 * math.Sin(2*math.Pi*t.hz*float64(t.at)/48000))
		b[n], b[n+1] = v, v
		t.at++
	}
	if t.at >= t.len {
		return n, io.EOF
	}
	return n, nil
}

// readAll reads d to its end, its left channel.
func readAll(t *testing.T, s *shifter) []float32 {
	t.Helper()
	var left []float32
	buf := make([]float32, 1000)
	for {
		n, err := s.Read(buf)
		for i := 0; i < n; i += 2 {
			left = append(left, buf[i])
		}
		if err == io.EOF {
			return left
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(left) > 10*48000 {
			t.Fatal("no end")
		}
	}
}

// pitch is the frequency of a sine, by how often it crosses zero going up.
func pitch(x []float32) float64 {
	ups := 0
	for i := 1; i < len(x); i++ {
		if x[i-1] < 0 && x[i] >= 0 {
			ups++
		}
	}
	return float64(ups) * 48000 / float64(len(x))
}

// Shifting raises the pitch by the ratio and keeps the length, so that,
// slowed by the speed, a song sounds at its own pitch.
func TestShifterRaisesPitchKeepingLength(t *testing.T) {
	src := &tone{hz: 440, len: 3 * 48000}
	s := newShifter(src)
	s.setRatio(1 / 0.75)
	s.SetPosition(0)
	out := readAll(t, s)
	if len(out) != int(src.len) {
		t.Errorf("%d frames, want %d", len(out), src.len)
	}
	// Away from the ends, where the windows fade in and out.
	if hz := pitch(out[4800 : len(out)-4800]); math.Abs(hz-440/0.75) > 6 {
		t.Errorf("pitch %.1f Hz, want %.1f", hz, 440/0.75)
	}
}

// At a ratio of 1 the sound passes through as it was.
func TestShifterPassesThrough(t *testing.T) {
	s := newShifter(&tone{hz: 440, len: 48000})
	s.SetPosition(0)
	want := readAll(t, newShifter(&tone{hz: 440, len: 48000}))
	got := readAll(t, s)
	if len(got) != len(want) {
		t.Fatalf("%d frames, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("frame %d changed", i)
		}
	}
}

// A ratio set takes effect where the shifter moves to next, and the
// shifter gives the rest of the sound from there.
func TestShifterRatioFromPosition(t *testing.T) {
	src := &tone{hz: 440, len: 3 * 48000}
	s := newShifter(src)
	s.setRatio(1 / 0.9)
	buf := make([]float32, 2000)
	s.Read(buf)
	if s.ratio != 1 {
		t.Fatal("ratio changed before moving")
	}
	s.SetPosition(48000)
	out := readAll(t, s)
	if len(out) != 2*48000 {
		t.Errorf("%d frames from 1 s, want %d", len(out), 2*48000)
	}
	if hz := pitch(out[4800 : len(out)-4800]); math.Abs(hz-440/0.9) > 6 {
		t.Errorf("pitch %.1f Hz, want %.1f", hz, 440/0.9)
	}
}
