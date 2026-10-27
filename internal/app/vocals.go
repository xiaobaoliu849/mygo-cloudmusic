package app

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"

	"netease-music-mygo/internal/player"
	"netease-music-mygo/internal/speech"
)

// A lesson can play a line's singing alone, the singer's voice without
// the band, separated on the device: the nearest the teacher comes to
// singing it, as the voice it talks in cannot sing.

// These find, download and run the model that separates the singing;
// tests replace them.
var (
	vocalsInstalled = speech.VocalsInstalled
	downloadVocals  = speech.DownloadVocals
	removeVocals    = speech.RemoveVocals
	newSeparator    = func(dir string) (separator, error) { return speech.NewSeparator(dir) }
)

// A separator separates the singing from a song's music (speech.Separator).
type separator interface {
	Vocals(samples []float32, ch, rate int) ([]float32, int, error)
	Close()
}

// The model loaded, kept once loaded, and the lock one separation at a
// time holds it under.
var separating struct {
	sync.Mutex
	s separator
}

// vocalsWait is how long the teacher waits for a line's singing to
// separate and play, before it gives up on it.
const vocalsWait = time.Minute

// vocalsContext is how much of the song either side of a line is
// separated with it, and cut off after: the model hears where a voice
// comes in and goes out better with what is around it.
const vocalsContext = time.Second

// vocalsState is what the app has of the singing: the model, and the
// lines of the track playing separated so far.
type vocalsState struct {
	model modelState

	track int64            // the track the lines are of
	lines map[int]sungLine // a line's singing, by its index
	work  int              // the line being separated, plus one; 0 for none
	want  int              // the line to play once separated, plus one; 0 for none
	out   *audio.Player    // the singing playing
	err   string           // why the last line asked for did not play
}

// sungLine is a line's singing alone: interleaved stereo at rate, from
// where the line plays alone to where it pauses.
type sungLine struct {
	rate    int
	samples []float32
}

// vocalsReady reports whether the model is installed, looking once.
func (a *app) vocalsReady() bool {
	m := &a.vocals.model
	if !m.checked {
		m.checked = true
		if dir, err := modelDir(); err == nil {
			m.installed = vocalsInstalled(dir)
		}
	}
	return m.installed
}

// startVocalsDownload downloads the model, its progress shown as it goes.
func (a *app) startVocalsDownload() {
	m := &a.vocals.model
	if m.downloading || m.installed {
		return
	}
	dir, err := modelDir()
	if err != nil {
		m.err = "无法保存模型"
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.downloading, m.err, m.cancel = true, "", cancel
	m.done, m.total = 0, speech.VocalsSize()
	go func() {
		// Shown in steps of a megabyte, as the captions' model.
		var shown int64
		err := downloadVocals(ctx, dir, func(done, total int64) {
			if done-shown < 1<<20 && done < total {
				return
			}
			shown = done
			a.post(func() {
				m.done, m.total = done, total
				if a.settingsWin != nil {
					a.settingsWin.Invalidate()
				}
			})
		})
		a.post(func() {
			if a.settingsWin != nil {
				a.settingsWin.Invalidate()
			}
			m.downloading, m.cancel = false, nil
			switch {
			case err == nil:
				m.installed, m.checked = true, true
				// The captions' libraries may have come with it.
				a.captions.model.checked = false
			case ctx.Err() != nil:
			default:
				m.err = "下载失败，请检查网络后重试"
			}
		})
	}()
}

// cancelVocalsDownload stops the download, keeping what came in for the
// next.
func (a *app) cancelVocalsDownload() {
	if m := &a.vocals.model; m.cancel != nil {
		m.cancel()
	}
}

// deleteVocals stops the singing, lets the model go and removes it.
func (a *app) deleteVocals() {
	m := &a.vocals.model
	if !m.installed {
		return
	}
	a.stopVocals()
	m.installed = false
	dir, err := modelDir()
	if err != nil {
		return
	}
	go func() {
		separating.Lock()
		if separating.s != nil {
			separating.s.Close()
			separating.s = nil
		}
		removeVocals(dir)
		separating.Unlock()
	}()
}

// playVocals plays line n's singing alone, at the lesson's tempo and the
// song's pitch, the song paused: at once if separated already, else once
// it is. It reports false, doing nothing, without the model or a song to
// separate it from, saying why in the vocals' err.
func (a *app) playVocals(n int) bool {
	v := &a.vocals
	a.stopVocals()
	if !a.vocalsReady() {
		v.err = "还没有下载人声分离模型"
		return false
	}
	tr := a.pl.Current()
	from, to, ok := a.pl.LineSpan(n)
	if tr == nil || !ok {
		v.err = "没有这一句"
		return false
	}
	a.pauseSong()
	if v.track != tr.ID {
		v.track, v.lines, v.work = tr.ID, nil, 0
	}
	if l, ok := v.lines[n]; ok {
		a.singLine(l)
		return true
	}
	v.want = n + 1
	if v.work != 0 {
		// It plays once the line separating now is done, and this one.
		return true
	}
	dir, err := modelDir()
	if err != nil {
		v.want, v.err = 0, "找不到人声分离模型"
		return false
	}
	au, err := a.pl.OpenAudio()
	if err != nil {
		v.want, v.err = 0, "歌还没加载好"
		return false
	}
	v.work = n + 1
	track := tr.ID
	go func() {
		l, err := separateLine(au, from, to, dir)
		au.Close()
		a.post(func() {
			if v.track != track {
				return
			}
			v.work = 0
			if err != nil {
				if v.want == n+1 {
					v.want, v.err = 0, "人声分离失败"
				}
				return
			}
			if v.lines == nil {
				v.lines = map[int]sungLine{}
			}
			v.lines[n] = l
			switch want := v.want - 1; {
			case want == n:
				v.want = 0
				a.singLine(l)
			case want >= 0:
				v.want = 0
				a.playVocals(want)
			}
		})
	}()
	return true
}

// separateLine separates the singing of the song in au from from to to,
// with vocalsContext of it either side, cut off after, with the model in
// dir.
func separateLine(au player.Audio, from, to time.Duration, dir string) (sungLine, error) {
	rate, ch := au.SampleRate(), max(au.Channels(), 1)
	start := max(0, from-vocalsContext)
	if err := au.SetPosition(int64(start.Seconds() * float64(rate))); err != nil {
		return sungLine{}, err
	}
	n := int((to+vocalsContext-start).Seconds()*float64(rate)) * ch
	song := make([]float32, 0, n)
	buf := make([]float32, 4096*ch)
	for len(song) < n {
		got, err := au.Read(buf)
		song = append(song, buf[:min(got, n-len(song))]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return sungLine{}, err
		}
	}
	separating.Lock()
	defer separating.Unlock()
	if separating.s == nil {
		s, err := newSeparator(dir)
		if err != nil {
			return sungLine{}, err
		}
		separating.s = s
	}
	out, outRate, err := separating.s.Vocals(song, ch, rate)
	if err != nil {
		return sungLine{}, err
	}
	// Cut back to the line, at the rate the singing came out at.
	cut := func(d time.Duration) int { return min(int(d.Seconds()*float64(outRate))*2, len(out)) }
	return sungLine{rate: outRate, samples: out[cut(from-start):cut(to-start)]}, nil
}

// singLine plays a line's singing at the lesson's tempo, as loud as the
// song plays.
func (a *app) singLine(l sungLine) {
	v := &a.vocals
	out := player.PlayAtTempo(l.rate, 2, l.samples, a.pl.Tempo())
	out.SetVolume(a.volume * a.volume)
	v.out = out
	out.OnEnd(func() {
		a.post(func() {
			if v.out == out {
				out.Close()
				v.out = nil
			}
		})
	})
}

// pauseSong pauses the song, a line playing alone stopping as at its end.
func (a *app) pauseSong() {
	if !a.pl.Playing() {
		return
	}
	if _, alone := a.pl.Line(); alone || a.pl.SingingThrough() {
		a.pl.StopLine()
	} else {
		a.pl.TogglePlay()
	}
}

// vocalsPlaying reports whether a line's singing plays, or separates to
// play.
func (a *app) vocalsPlaying() bool {
	v := &a.vocals
	return v.want != 0 || v.out != nil && v.out.Playing()
}

// stopVocals stops the singing playing, and any to play once separated,
// what went wrong with the last told no more.
func (a *app) stopVocals() {
	v := &a.vocals
	v.want, v.err = 0, ""
	if v.out != nil {
		v.out.Close()
		v.out = nil
	}
}

// vocalsPrompt tells the teacher it can play a line's singing alone, once
// the model that separates it is installed.
const vocalsPrompt = `

你虽然不会唱，但可以用 play_vocals 放原唱歌手单独的人声（去掉了伴奏），当作你的示范：学生听不清某句怎么唱、旋律被伴奏盖住、或者说"你唱给我听"时，就用它，速度也跟着 slow_down 设的走。它分离和播完才返回，返回前不要说话。`
