// Package mic records from the default microphone, for the listener to
// hear themselves sing a line of a lesson.
package mic

import (
	"errors"
	"math"
)

// Rate is the frames a second recorded: mono.
const Rate = 48000

// ErrUnsupported says the system has no recording here yet.
var ErrUnsupported = errors.New("mic: recording is not supported on this system")

// A Recorder records until stopped.
type Recorder struct {
	stop func()
}

// Start records from the default microphone, giving got each stretch of
// mono samples, at Rate, on a goroutine of its own, until Stop.
func Start(got func([]float32)) (*Recorder, error) {
	stop, err := start(got)
	if err != nil {
		return nil, err
	}
	return &Recorder{stop: stop}, nil
}

// Stop stops recording, once got has had the last of it.
func (r *Recorder) Stop() {
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
}

// Level returns how loud samples are, from 0 to 1, as a meter shows it:
// their peak, in decibels from -60 up.
func Level(samples []float32) float32 {
	var peak float32
	for _, s := range samples {
		peak = max(peak, float32(math.Abs(float64(s))))
	}
	if peak <= 0 {
		return 0
	}
	db := 20 * math.Log10(float64(peak))
	return float32(min(max((db+60)/60, 0), 1))
}

// Normalize returns a copy of samples brought up so their peak is near
// full, but by no more than 8 times, as a voice recorded far off would
// come out of the noise.
func Normalize(samples []float32) []float32 {
	var peak float32
	for _, s := range samples {
		peak = max(peak, float32(math.Abs(float64(s))))
	}
	gain := float32(1)
	if peak > 0 {
		gain = min(0.9/peak, 8)
	}
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = s * gain
	}
	return out
}
