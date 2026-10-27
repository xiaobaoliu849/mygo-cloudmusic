package mic

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// phrases is a voice singing, bursts of sound and pauses between them,
// as long as d at rate, a seed choosing when.
func phrases(d time.Duration, rate int, seed uint64) []float32 {
	r := rand.New(rand.NewPCG(seed, 1))
	out := make([]float32, int(d.Seconds()*float64(rate)))
	for i := 0; i < len(out); {
		on := int((0.3 + r.Float64()) * float64(rate))
		hz := 200 + r.Float64()*400
		for k := 0; k < on && i < len(out); k, i = k+1, i+1 {
			out[i] = float32(0.3 * math.Sin(2*math.Pi*hz*float64(k)/float64(rate)))
		}
		i += int((0.1 + 0.5*r.Float64()) * float64(rate))
	}
	return out
}

// A take sung along with a song lines up with it where the voice comes
// and goes as the song's does, and Align says how far from where the take
// was thought to start.
func TestAlign(t *testing.T) {
	const songRate = 44100
	song := phrases(50*time.Second, songRate, 7)
	// The take starts 10 s in, thought to start 350 ms later than it did.
	start := 10 * time.Second
	thought := start + 350*time.Millisecond
	voice := make([]float32, 30*Rate)
	for i := range voice {
		j := int((start.Seconds() + float64(i)/Rate) * songRate)
		voice[i] = 0.5 * song[j]
	}
	reach := time.Second
	from := int((thought - reach).Seconds() * songRate)
	lag, ok := Align(voice, song[from:from+int((30*time.Second+2*reach).Seconds()*songRate)], songRate, reach)
	if !ok {
		t.Fatal("no clear match")
	}
	if want := start - thought; lag < want-20*time.Millisecond || lag > want+20*time.Millisecond {
		t.Errorf("lag %v, want %v", lag, want)
	}
}

// A take lines up with a song that ends short of the take and reach after
// it: as a take sung to the song's end does, or a song read a sample
// short in rounding. Align used to refuse any song shorter, which every
// take sung to the end, and one in eight others, was.
func TestAlignSongEndsShort(t *testing.T) {
	const songRate = 44100
	// The take is 30 s from 10 s in, thought to start 350 ms later.
	song := phrases(45*time.Second, songRate, 7)
	start := 10 * time.Second
	thought := start + 350*time.Millisecond
	voice := make([]float32, 30*Rate)
	for i := range voice {
		voice[i] = 0.5 * song[int((start.Seconds()+float64(i)/Rate)*songRate)]
	}
	reach := time.Second
	from := int((thought - reach).Seconds() * songRate)
	full := int((30*time.Second + 2*reach).Seconds() * songRate)
	for name, end := range map[string]int{
		"a sample short":       from + full - 1,
		"ending with the take": int((start + 30*time.Second).Seconds() * songRate),
	} {
		lag, ok := Align(voice, song[from:end], songRate, reach)
		if !ok {
			t.Errorf("%s: no clear match", name)
			continue
		}
		if want := start - thought; lag < want-20*time.Millisecond || lag > want+20*time.Millisecond {
			t.Errorf("%s: lag %v, want %v", name, lag, want)
		}
	}
}

// A take that is not of the song matches nothing.
func TestAlignNothing(t *testing.T) {
	song := phrases(40*time.Second, 44100, 7)
	voice := phrases(35*time.Second, Rate, 99)
	if lag, ok := Align(voice, song, 44100, time.Second); ok {
		t.Errorf("matched at %v", lag)
	}
}

// Brought down to 16 kHz, a voice's tone keeps its loudness, chunk after
// chunk as the microphone gives them, while a tone too high for 16 kHz
// to hold goes, rather than folding back as a false tone.
func TestDownsampler(t *testing.T) {
	rms := func(hz float64) float64 {
		d := NewDownsampler(16000)
		var out []float32
		for c := range 10 {
			chunk := make([]float32, Rate/10)
			for i := range chunk {
				chunk[i] = float32(0.5 * math.Sin(2*math.Pi*hz*float64(c*len(chunk)+i)/Rate))
			}
			out = append(out, d.Next(chunk)...)
		}
		if len(out) != 16000 {
			t.Fatalf("%v Hz: %d samples a second, want 16000", hz, len(out))
		}
		var sum float64
		for _, s := range out[1600:] {
			sum += float64(s) * float64(s)
		}
		return math.Sqrt(sum / float64(len(out)-1600))
	}
	if r := rms(440); r < 0.33 || r > 0.38 {
		t.Errorf("440 Hz came out at %.3f, want 0.354", r)
	}
	if r := rms(12000); r > 0.02 {
		t.Errorf("12 kHz came out at %.3f, want it gone", r)
	}
}
