package mic

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"testing"
)

// A take written as WAV says its length and format, and keeps its
// samples, clipped to full.
func TestWriteWAV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteWAV(&b, []float32{0, 0.5, -1, 2}); err != nil {
		t.Fatal(err)
	}
	d := b.Bytes()
	le := binary.LittleEndian
	if len(d) != 44+8 || string(d[:4]) != "RIFF" || string(d[8:12]) != "WAVE" {
		t.Fatalf("header %q, %d bytes", d[:12], len(d))
	}
	if le.Uint32(d[24:]) != Rate || le.Uint16(d[22:]) != 1 || le.Uint32(d[40:]) != 8 {
		t.Errorf("rate %d, channels %d, data %d", le.Uint32(d[24:]), le.Uint16(d[22:]), le.Uint32(d[40:]))
	}
	for i, want := range []int16{0, 16383, -32767, 32767} {
		if got := int16(le.Uint16(d[44+2*i:])); got != want {
			t.Errorf("sample %d: %d, want %d", i, got, want)
		}
	}
}

// A take plays back as loud as a voice sung near the microphone, by its
// loudness, whatever its peak: a quiet one up to 12 times, rounded off
// short of full rather than clipping.
func TestNormalize(t *testing.T) {
	rms := func(amp float32) float64 {
		s := make([]float32, Rate)
		for i := range s {
			s[i] = amp * float32(math.Sin(2*math.Pi*220*float64(i)/Rate))
		}
		var e float64
		for _, v := range Normalize(s) {
			e += float64(v) * float64(v)
		}
		return math.Sqrt(e / Rate)
	}
	if got := rms(0.02); math.Abs(got-voiceRMS) > 0.003 {
		t.Errorf("a quiet take at %.3f, want %.3f", got, voiceRMS)
	}
	if got, want := rms(0.008), 0.008*12/math.Sqrt2; math.Abs(got-want) > 0.003 {
		t.Errorf("a very quiet take at %.3f, want brought up 12 times, %.3f", got, want)
	}
	loud := []float32{0.9, -0.9, 0.9, -0.9}
	loud = append(loud, make([]float32, Rate/50)...)
	for _, s := range Normalize(slices.Repeat(loud, 100)) {
		if s > 1 || s < -1 {
			t.Fatalf("clipped at %v", s)
		}
	}
}

// Where the microphone cut the voice to nothing, it fades out and in
// rather than stepping, which clicks.
func TestDeclick(t *testing.T) {
	s := make([]float32, 3000)
	for i := range s {
		s[i] = 0.5
	}
	clear(s[1000:2000])
	out := declick(s)
	if out[999] > 0.05 || out[2000] > 0.05 {
		t.Errorf("steps at the silence's edges: %v, %v", out[999], out[2000])
	}
	if out[500] != 0.5 || out[2500] != 0.5 {
		t.Error("changed the voice away from the silence")
	}
}
