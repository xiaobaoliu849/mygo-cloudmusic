package teacher

import (
	"io"
	"sync"
	"time"
)

// A Voice is what the model says, played as it comes: an audio.Decoder,
// mono at OutRate, that waits for more rather than playing silence ahead
// of it, so speech plays as soon as it comes, until Close.
type Voice struct {
	mu     sync.Mutex
	cond   sync.Cond
	queued []float32
	said   int64 // frames given out to play, since the start
	closed bool
}

// NewVoice returns a Voice with nothing to say yet.
func NewVoice() *Voice {
	v := &Voice{}
	v.cond.L = &v.mu
	return v
}

// Say queues samples, mono at OutRate, to play after what is queued.
func (v *Voice) Say(samples []float32) {
	v.mu.Lock()
	v.queued = append(v.queued, samples...)
	v.mu.Unlock()
	v.cond.Signal()
}

// Hush drops what is queued and not yet played.
func (v *Voice) Hush() {
	v.mu.Lock()
	v.queued = nil
	v.mu.Unlock()
}

// Speaking reports whether, with heard of it played so far, as the audio
// player's position says, there is more of what was said to play.
func (v *Voice) Speaking(heard time.Duration) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.queued) > 0 || heard+heardSlack < time.Duration(v.said)*time.Second/OutRate
}

// heardSlack is how far short of the end of what was said the audio
// player's position may stop, all of it heard: it reads a few frames
// ahead of where it plays, to smooth them, and its position stops there.
const heardSlack = 20 * time.Millisecond

// Close ends the Voice: the player playing it comes to its end.
func (v *Voice) Close() {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	v.cond.Broadcast()
}

func (v *Voice) SampleRate() int               { return OutRate }
func (v *Voice) Channels() int                 { return 1 }
func (v *Voice) Len() int64                    { return -1 }
func (v *Voice) SetPosition(frame int64) error { return nil }

// Read gives out what is queued, waiting for some if none is.
func (v *Voice) Read(buf []float32) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for len(v.queued) == 0 && !v.closed {
		v.cond.Wait()
	}
	if v.closed {
		return 0, io.EOF
	}
	n := copy(buf, v.queued)
	v.queued = v.queued[n:]
	v.said += int64(n)
	return n, nil
}
