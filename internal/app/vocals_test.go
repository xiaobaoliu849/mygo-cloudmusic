package app

import (
	"io"
	"math"
	"testing"
	"time"
)

// timeSong is a song whose every sample is the time it plays at, in
// seconds, in stereo at 48 kHz, as player.Audio.
type timeSong struct{ at, len int64 }

func (c *timeSong) SampleRate() int { return 48000 }
func (c *timeSong) Channels() int   { return 2 }
func (c *timeSong) Close() error    { return nil }
func (c *timeSong) SetPosition(f int64) error {
	c.at = f
	return nil
}
func (c *timeSong) Read(b []float32) (int, error) {
	n := 0
	for ; n+1 < len(b) && c.at < c.len; n += 2 {
		b[n], b[n+1] = float32(c.at)/48000, float32(c.at)/48000
		c.at++
	}
	if c.at >= c.len {
		return n, io.EOF
	}
	return n, nil
}

// resampling passes the song through as the model does its singing:
// stereo at 44.1 kHz, as long as the song.
type resampling struct{}

func (resampling) Close() {}
func (resampling) Vocals(x []float32, ch, rate int) ([]float32, int, error) {
	n := len(x) / ch * 44100 / rate
	out := make([]float32, 2*n)
	for i := range n {
		v := x[i*rate/44100*ch]
		out[2*i], out[2*i+1] = v, v
	}
	return out, 44100, nil
}

// A line's singing is separated with the song around it, then cut back
// to the line, at the rate the model sings at, also where the line starts
// too early in the song for all of the context, or ends past its end.
func TestSeparateLineCutsToLine(t *testing.T) {
	old := newSeparator
	defer func() { newSeparator = old; separating.s = nil }()
	newSeparator = func(string) (separator, error) { return resampling{}, nil }
	separating.s = nil
	for _, span := range [][2]time.Duration{{5 * time.Second, 9 * time.Second}, {300 * time.Millisecond, 2 * time.Second}, {57 * time.Second, 61 * time.Second}} {
		from, to := span[0], span[1]
		l, err := separateLine(&timeSong{len: 60 * 48000}, from, to, "")
		if err != nil {
			t.Fatal(err)
		}
		end := min(to, 60*time.Second)
		if l.rate != 44100 || math.Abs(float64(len(l.samples)/2)/44100-(end-from).Seconds()) > 0.001 {
			t.Errorf("%v–%v: %d frames at %d", from, to, len(l.samples)/2, l.rate)
			continue
		}
		first, last := l.samples[0], l.samples[len(l.samples)-1]
		if math.Abs(float64(first)-from.Seconds()) > 0.001 || math.Abs(float64(last)-end.Seconds()) > 0.001 {
			t.Errorf("%v–%v: the singing runs from %.3fs to %.3fs", from, to, first, last)
		}
	}
}
