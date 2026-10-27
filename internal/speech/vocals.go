package speech

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"
)

// The singing of a song, apart from its music: for a lesson to play the
// singer's voice alone, as the teacher's demonstration of a line. UVR's
// MDX-Net separates it, through sherpa-onnx's libraries, those captions
// run on.

// The structs of sherpa-onnx's source separation, as of the version the
// app ships (scripts/sherpa).

type cSpleeter struct{ vocals, accompaniment *byte }

type cSeparationModelConfig struct {
	spleeter   cSpleeter
	uvr        cOne
	numThreads int32
	debug      int32
	provider   *byte
}

type cSeparationConfig struct{ model cSeparationModelConfig }

type cStem struct {
	samples     **float32 // a channel's samples each
	numChannels int32
	n           int32
}

type cSeparation struct {
	stems      *cStem
	numStems   int32
	sampleRate int32
}

var separation struct {
	create  func(*cSeparationConfig) uintptr
	destroy func(uintptr)
	process func(ss uintptr, samples **float32, channels, n, rate int32) *cSeparation
	free    func(*cSeparation)
}

// VocalsName names the model that separates the singing.
const VocalsName = "UVR_MDXNET_9482.onnx"

// vocalsFile is the model, as k2-fsa publishes it for sherpa-onnx.
var vocalsFile = modelFile{path: VocalsName, size: 29704738, sha256: "9d78f8566fa8198065214ab628be1de966a500c57786695aa4b13e2b27a7727d"}

// vocalsURL is where the model downloads from; tests replace it.
var vocalsURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/source-separation-models/" + VocalsName

// VocalsSize is how many bytes DownloadVocals downloads: the model, and
// the libraries that run it.
func VocalsSize() int64 {
	n := vocalsFile.size
	for _, f := range natives() {
		n += f.gzSize
	}
	return n
}

// VocalsInstalled reports whether the model that separates the singing
// and its libraries are in root, whole.
func VocalsInstalled(root string) bool {
	if !Available() {
		return false
	}
	info, err := os.Stat(filepath.Join(root, VocalsName))
	return err == nil && info.Size() == vocalsFile.size && libsInstalled(root)
}

// DownloadVocals downloads the model that separates the singing and the
// libraries that run it into root, as Download does the captions'.
func DownloadVocals(ctx context.Context, root string, progress func(done, total int64)) error {
	if !Available() {
		return ErrUnavailable
	}
	total := VocalsSize()
	before, err := downloadNatives(ctx, root, total, progress)
	if err != nil {
		return err
	}
	path := filepath.Join(root, VocalsName)
	if info, err := os.Stat(path); err == nil && info.Size() == vocalsFile.size {
		progress(total, total)
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := fetch(ctx, vocalsURL, path, vocalsFile, func(n int64) { progress(before+n, total) }); err != nil {
		return err
	}
	progress(total, total)
	return nil
}

// RemoveVocals removes the model that separates the singing from root,
// and the libraries, unless captions still run on them.
func RemoveVocals(root string) error {
	err := os.Remove(filepath.Join(root, VocalsName))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if !modelIn(root) {
		if e := os.RemoveAll(libDir(root)); err == nil {
			err = e
		}
	}
	return err
}

// A Separator separates the singing from a song's music, one Vocals at a
// time. It holds the model until it is closed.
type Separator struct {
	mu sync.Mutex
	ss uintptr
}

// NewSeparator loads the model and the libraries DownloadVocals put in
// root.
func NewSeparator(root string) (*Separator, error) {
	return newSeparator(filepath.Join(root, VocalsName), libDir(root))
}

func newSeparator(model, libs string) (*Separator, error) {
	if err := load(libs); err != nil {
		return nil, err
	}
	var cs cstrings
	cfg := cSeparationConfig{model: cSeparationModelConfig{
		uvr: cOne{cs.of(model)},
		// As many threads as the recognizer: it runs while the listener
		// waits, and more gain little.
		numThreads: int32(min(max(runtime.NumCPU()/2, 1), 4)),
		provider:   cs.of("cpu"),
	}}
	ss := separation.create(&cfg)
	runtime.KeepAlive(cs)
	if ss == 0 {
		return nil, errors.New("speech: the model would not load")
	}
	return &Separator{ss: ss}, nil
}

// Close lets the model go.
func (s *Separator) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ss != 0 {
		separation.destroy(s.ss)
	}
	s.ss = 0
}

// Vocals returns the singing in samples, interleaved, of ch channels at
// rate: interleaved stereo, at the rate it returns, as long as samples.
func (s *Separator) Vocals(samples []float32, ch, rate int) ([]float32, int, error) {
	ch = max(ch, 1)
	n := len(samples) / ch
	if n == 0 {
		return nil, rate, nil
	}
	// The model hears stereo: a mono song is the same on both sides.
	in := [2][]float32{make([]float32, n), make([]float32, n)}
	for i := range n {
		in[0][i] = samples[i*ch]
		in[1][i] = samples[i*ch+min(1, ch-1)]
	}
	ptrs := []*float32{&in[0][0], &in[1][0]}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ss == 0 {
		return nil, 0, errors.New("speech: the separator is closed")
	}
	out := separation.process(s.ss, &ptrs[0], 2, int32(n), int32(rate))
	runtime.KeepAlive(in)
	if out == nil || out.numStems == 0 {
		return nil, 0, errors.New("speech: the singing would not separate")
	}
	defer separation.free(out)
	// The vocals are the first stem.
	v := unsafe.Slice(out.stems, out.numStems)[0]
	chans := unsafe.Slice(v.samples, v.numChannels)
	vch := len(chans)
	left := unsafe.Slice(chans[0], v.n)
	right := unsafe.Slice(chans[min(1, vch-1)], v.n)
	stereo := make([]float32, 2*int(v.n))
	for i := range int(v.n) {
		stereo[2*i], stereo[2*i+1] = left[i], right[i]
	}
	return stereo, int(out.sampleRate), nil
}
