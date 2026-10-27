package mic

import (
	"math"
	"time"
)

// alignStep is the step in time Align compares in.
const alignStep = 10 * time.Millisecond

// Align returns how much later in a song a take sung along with it lies
// than where it was thought to start: comparing where sound comes in, in
// the voice and in the song, in 10 ms steps, within reach either way.
// Voice is the take, at Rate; song is the song, mono at songRate, from
// reach before where the take was thought to start, and as long as the
// take and reach after it, or shorter, where the song ends: what is
// missing counts as silence. It reports false where nothing matches
// clearly: unless each third of the take, on its own, lines up where the
// whole does.
func Align(voice []float32, song []float32, songRate int, reach time.Duration) (time.Duration, bool) {
	a, b := envelope(song, songRate), envelope(voice, Rate)
	r := int(reach / alignStep)
	if len(b) < 3*alignPart {
		return 0, false
	}
	if short := len(b) + 2*r - len(a); short > 0 {
		a = append(a, make([]float64, short)...)
	}
	standardize(a)
	standardize(b)
	whole := bestLag(a, b, 0, r)
	third := len(b) / 3
	for p := range 3 {
		lag := bestLag(a, b[p*third:(p+1)*third], p*third, r)
		if lag < whole-alignAgree || lag > whole+alignAgree {
			return 0, false
		}
	}
	return time.Duration(whole) * alignStep, true
}

// alignPart is the fewest steps a third of a take Align lines up takes:
// ten seconds, enough phrases to tell where it goes.
const alignPart = 1000

// alignAgree is how many steps apart the thirds of a take may line up,
// and still agree.
const alignAgree = 6

// bestLag returns the lag, within r steps either way, at which b best
// matches a, b being at off steps into the take a starts r steps before.
func bestLag(a, b []float64, off, r int) int {
	best, top := 0, math.Inf(-1)
	for lag := -r; lag <= r; lag++ {
		k := r + off + lag
		if k < 0 || k+len(b) > len(a) {
			continue
		}
		var dot float64
		for i, v := range b {
			dot += a[k+i] * v
		}
		if dot > top {
			best, top = lag, dot
		}
	}
	return best
}

// envelope returns where sound comes in, in samples at rate, where a
// voice sings, from 250 to 2500 Hz, each alignStep.
func envelope(samples []float32, rate int) []float64 {
	hp := newBiquad(rate, 250, true)
	lp := newBiquad(rate, 2500, false)
	step := rate * int(alignStep/time.Millisecond) / 1000
	out := make([]float64, 0, len(samples)/step+1)
	var e, last float64
	for i, s := range samples {
		v := lp.next(hp.next(float64(s)))
		e += v * v
		if (i+1)%step == 0 {
			// Where sound comes in: how much louder than a step before, in
			// decibels, rather than how loud, which a sung note holds for
			// longer than the steps Align tells apart.
			db := 10 * math.Log10(e/float64(step)+1e-10)
			out = append(out, max(0, db-last))
			last, e = db, 0
		}
	}
	return out
}

// standardize makes x's mean 0 and its deviation 1.
func standardize(x []float64) {
	var sum, sq float64
	for _, v := range x {
		sum += v
	}
	mean := sum / float64(len(x))
	for _, v := range x {
		sq += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(sq / float64(len(x)))
	if sd == 0 {
		sd = 1
	}
	for i := range x {
		x[i] = (x[i] - mean) / sd
	}
}

// A biquad is a second-order filter, high- or low-pass, as the Audio EQ
// Cookbook gives them.
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func newBiquad(rate int, hz float64, high bool) *biquad {
	w := 2 * math.Pi * hz / float64(rate)
	cos, alpha := math.Cos(w), math.Sin(w)/math.Sqrt2 // Q of 1/√2
	a0 := 1 + alpha
	f := &biquad{a1: -2 * cos / a0, a2: (1 - alpha) / a0}
	if high {
		f.b0, f.b1, f.b2 = (1+cos)/2/a0, -(1+cos)/a0, (1+cos)/2/a0
	} else {
		f.b0, f.b1, f.b2 = (1-cos)/2/a0, (1-cos)/a0, (1-cos)/2/a0
	}
	return f
}

func (f *biquad) next(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1, f.y2, f.y1 = f.x1, x, f.y1, y
	return y
}

// A Downsampler brings what the microphone hears, at Rate, down to a
// rate Rate is a whole number of times, filtering out first what is too
// high for that rate to hold, from one call to the next of a recording.
type Downsampler struct {
	by, at int
	lp     [2]*biquad
}

// NewDownsampler returns a Downsampler to rate, which must divide Rate.
func NewDownsampler(rate int) *Downsampler {
	// Well short of half the rate, twice over, for a steeper fall past
	// it: a voice is heard, and understood, by what is below.
	cut := 0.35 * float64(rate)
	return &Downsampler{by: Rate / rate, lp: [2]*biquad{newBiquad(Rate, cut, false), newBiquad(Rate, cut, false)}}
}

// Next returns samples at Rate brought down, going on from the last.
func (d *Downsampler) Next(samples []float32) []float32 {
	out := make([]float32, 0, len(samples)/d.by+1)
	for _, s := range samples {
		v := d.lp[1].next(d.lp[0].next(float64(s)))
		if d.at == 0 {
			out = append(out, float32(v))
		}
		d.at = (d.at + 1) % d.by
	}
	return out
}
