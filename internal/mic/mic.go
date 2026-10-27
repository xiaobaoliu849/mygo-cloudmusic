// Package mic records from the default microphone, for the listener to
// hear themselves sing a line of a lesson.
package mic

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
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

// Normalize returns a copy of samples to play back: as loud as a voice
// sung near the microphone, by the loudness of what is sung, not its
// peak, up to 12 times; its peaks rounded off short of full; and the
// edges smoothed where a microphone that silences what it takes for
// noise cut it in and out, which click.
func Normalize(samples []float32) []float32 {
	out := declick(samples)
	// The loudness of what is sung: of the 20 ms stretches above -50 dB.
	const frame = Rate / 50
	var sum float64
	n := 0
	for i := 0; i+frame <= len(out); i += frame {
		var e float64
		for _, s := range out[i : i+frame] {
			e += float64(s) * float64(s)
		}
		if e/frame > 1e-5 {
			sum += e
			n += frame
		}
	}
	gain := 1.0
	if n > 0 {
		gain = min(voiceRMS/math.Sqrt(sum/float64(n)), 12)
	}
	for i, s := range out {
		out[i] = float32(limit(float64(s) * gain))
	}
	return out
}

// voiceRMS is how loud a take plays back: -18 dB, as a voice sung near
// the microphone records.
const voiceRMS = 0.126

// limit rounds off a sample's peaks above 0.7 towards full, without
// clipping.
func limit(s float64) float64 {
	const knee = 0.7
	switch {
	case s > knee:
		return knee + (1-knee)*math.Tanh((s-knee)/(1-knee))
	case s < -knee:
		return -knee - (1-knee)*math.Tanh((-s-knee)/(1-knee))
	}
	return s
}

// declick returns a copy of samples faded over a few milliseconds into
// and out of each stretch of silence, exactly nothing, at least that
// long: where a microphone's noise gate cut the voice off.
func declick(samples []float32) []float32 {
	const fade = Rate * 3 / 1000
	out := slices.Clone(samples)
	for i := 0; i < len(out); {
		if out[i] != 0 {
			i++
			continue
		}
		j := i
		for j < len(out) && out[j] == 0 {
			j++
		}
		if j-i >= fade {
			for k := 1; k <= fade; k++ {
				g := float32(k) / fade
				if i-k >= 0 {
					out[i-k] *= g
				}
				if j+k-1 < len(out) {
					out[j+k-1] *= g
				}
			}
		}
		i = j
	}
	return out
}

// WriteWAV writes samples, mono at Rate, as a 16-bit WAV file.
func WriteWAV(w io.Writer, samples []float32) error {
	data := make([]byte, 44+2*len(samples))
	le := binary.LittleEndian
	copy(data, "RIFF")
	le.PutUint32(data[4:], uint32(36+2*len(samples)))
	copy(data[8:], "WAVEfmt ")
	le.PutUint32(data[16:], 16)
	le.PutUint16(data[20:], 1) // PCM
	le.PutUint16(data[22:], 1) // mono
	le.PutUint32(data[24:], Rate)
	le.PutUint32(data[28:], Rate*2)
	le.PutUint16(data[32:], 2)
	le.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	le.PutUint32(data[40:], uint32(2*len(samples)))
	for i, s := range samples {
		le.PutUint16(data[44+2*i:], uint16(int16(min(max(s, -1), 1)*32767)))
	}
	_, err := w.Write(data)
	return err
}
