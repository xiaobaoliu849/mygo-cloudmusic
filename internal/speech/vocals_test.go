package speech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The model that separates the singing downloads with the libraries
// captions run on, and removing either model leaves them for the other.
func TestVocalsShareLibraries(t *testing.T) {
	files := map[string][]byte{"model.onnx": []byte("asr"), "tokenizer/vocab.json": []byte("{}")}
	uvr := bytes.Repeat([]byte("uvr"), 1000)
	src := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lib.gz":
			w.Write(gzipped(lib))
		case "/uvr":
			w.Write(uvr)
		default:
			w.Write(files[r.URL.Path[len("/repo/"):]])
		}
	})
	withModel(t, files, src(modelFile{from: "lib.gz"}), src)
	oldFile, oldURL := vocalsFile, vocalsURL
	t.Cleanup(func() { vocalsFile, vocalsURL = oldFile, oldURL })
	sum := sha256.Sum256(uvr)
	vocalsFile = modelFile{path: VocalsName, size: int64(len(uvr)), sha256: hex.EncodeToString(sum[:])}
	vocalsURL = src(modelFile{from: "uvr"})

	root := t.TempDir()
	var last, total int64
	if err := DownloadVocals(context.Background(), root, func(d, t int64) { last, total = d, t }); err != nil {
		t.Fatal(err)
	}
	if !VocalsInstalled(root) || Installed(root) {
		t.Fatal("the vocals' model should be installed, and only it")
	}
	if last != total || total != VocalsSize() {
		t.Errorf("progress %d of %d", last, total)
	}
	if err := Download(context.Background(), root, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	Remove(root)
	if !VocalsInstalled(root) {
		t.Error("removing the captions' model took the libraries the vocals' needs")
	}
	Download(context.Background(), root, func(int64, int64) {})
	RemoveVocals(root)
	if !Installed(root) {
		t.Error("removing the vocals' model took the libraries captions need")
	}
	Remove(root)
	if _, err := os.Stat(libDir(root)); err == nil {
		t.Error("the libraries stay with neither model")
	}
}

// A song separates into singing as long as it, and quieter, with little
// of the bass under it. It needs a WAV of a song in VOCALS_WAV, the
// libraries in SHERPA_ONNX_DIR and the model in VOCALS_MODEL.
func TestVocals(t *testing.T) {
	wav, model, libs := os.Getenv("VOCALS_WAV"), os.Getenv("VOCALS_MODEL"), os.Getenv("SHERPA_ONNX_DIR")
	if wav == "" || model == "" || libs == "" {
		t.Skip("set VOCALS_WAV, VOCALS_MODEL and SHERPA_ONNX_DIR")
	}
	s, err := newSeparator(model, libs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	src := openWAV(t, wav)
	var song []float32
	buf := make([]float32, 8192)
	for len(song) < 5*src.rate*src.ch {
		n, err := src.Read(buf)
		song = append(song, buf[:n]...)
		if err == io.EOF {
			break
		}
	}
	start := time.Now()
	out, rate, err := s.Vocals(song, src.ch, src.rate)
	if err != nil {
		t.Fatal(err)
	}
	in := float64(len(song)/src.ch) / float64(src.rate)
	t.Logf("%.1fs separated in %v", in, time.Since(start))
	if got := float64(len(out)/2) / float64(rate); math.Abs(got-in) > 0.01 {
		t.Errorf("%.3fs of singing from %.3fs of song", got, in)
	}
	if rms(out) >= rms(song) || rms(out) == 0 {
		t.Errorf("singing at %.3f, the song at %.3f", rms(out), rms(song))
	}
	if os.Getenv("VOCALS_OUT") != "" {
		writeWAV(t, filepath.Join(os.Getenv("VOCALS_OUT"), "vocals.wav"), out, rate)
	}
}

// writeWAV writes interleaved stereo as 16-bit PCM, to listen to.
func writeWAV(t *testing.T, path string, x []float32, rate int) {
	var b bytes.Buffer
	le := func(v any) {
		switch v := v.(type) {
		case uint32:
			b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
		case uint16:
			b.Write([]byte{byte(v), byte(v >> 8)})
		}
	}
	b.WriteString("RIFF")
	le(uint32(36 + 2*len(x)))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(2))
	le(uint32(rate))
	le(uint32(rate * 4))
	le(uint16(4))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(2 * len(x)))
	for _, v := range x {
		le(uint16(int16(max(-1, min(1, v)) * 32767)))
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
