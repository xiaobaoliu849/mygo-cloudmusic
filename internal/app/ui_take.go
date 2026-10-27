package app

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
	"netease-music-mygo/internal/player"
)

// startMic records from the microphone, giving got what it hears.
var startMic = func(got func([]float32)) (interface{ Stop() }, error) {
	r, err := mic.Start(got)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// A micShare is the microphone, recorded once, shared among those that
// listen to it: the teacher, all along, and a take, as it records. Each
// recording the microphone of its own, a take recorded with the teacher
// on came out silent, and the teacher went deaf after it.
type micShare struct {
	mu   sync.Mutex
	rec  interface{ Stop() }
	subs map[int]func([]float32)
	next int
}

// microphone is the microphone the teacher and a take share, made the
// first time it is asked for.
func (a *app) microphone() *micShare {
	if a.sharedMic == nil {
		a.sharedMic = &micShare{}
	}
	return a.sharedMic
}

// listen gives got what the microphone hears, recording it if nothing
// else listens yet, until the listener it returns stops: the microphone
// with it, if nothing else listens.
func (m *micShare) listen(got func([]float32)) (interface{ Stop() }, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rec == nil {
		rec, err := startMic(m.hear)
		if err != nil {
			return nil, err
		}
		m.rec, m.subs = rec, map[int]func([]float32){}
	}
	m.next++
	m.subs[m.next] = got
	return &micListener{m, m.next}, nil
}

// hear gives each listener what the microphone heard.
func (m *micShare) hear(samples []float32) {
	m.mu.Lock()
	subs := make([]func([]float32), 0, len(m.subs))
	for _, got := range m.subs {
		subs = append(subs, got)
	}
	m.mu.Unlock()
	for _, got := range subs {
		got(samples)
	}
}

// A micListener is one listening to a micShare.
type micListener struct {
	m  *micShare
	id int
}

// Stop stops listening, and the microphone if nothing else listens.
func (l *micListener) Stop() {
	m := l.m
	m.mu.Lock()
	delete(m.subs, l.id)
	var rec interface{ Stop() }
	if len(m.subs) == 0 {
		rec, m.rec = m.rec, nil
	}
	m.mu.Unlock()
	// Not holding mu: stopping, the microphone hands over the last it
	// heard, to hear, which takes it.
	if rec != nil {
		rec.Stop()
	}
}

// throughTake is the key of the take of the song sung through, among
// those of its lines.
const throughTake = -1

// takes are what the listener sang in a lesson, recorded, for them to
// hear how they sang: the last take of each line, and of the song sung
// through.
type takes struct {
	sung map[int]*take // by line, or throughTake
	want bool          // the listener asked to record what is sung along next
	rec  *recording
	out  *audio.Player // a take playing back
	key  int           // the take playing back
	err  string        // why the last take went wrong, to show
	// aligning says a take is being lined up with the song, to play it
	// back once it is.
	aligning bool
}

// A take is the listener singing, recorded, and where in the song they
// sang it from, and at what tempo, to play the song under it as they
// heard it.
type take struct {
	samples []float32 // mono at mic.Rate
	at      time.Duration
	tempo   float64
}

// A recording is a take on its way: the microphone gives it samples on
// a goroutine of its own.
type recording struct {
	key   int
	limit int // samples to stop at
	stop  interface{ Stop() }
	take  *take // its samples come once it ends; where it starts, as it does

	mu      sync.Mutex
	samples []float32
	level   float32 // how loud the last of it was, for the meter
	full    bool
}

// takeLimit is how long a take of line l may go on: as long as the line
// plays at tempo, and a little, for coming in late.
func takeLimit(l lyrics.Line, tempo float64) time.Duration {
	d := time.Duration(float64(l.End-l.At)/tempo) + 2*time.Second
	return min(max(d, 4*time.Second), 30*time.Second)
}

// throughLimit is how long a take of a song length long, sung through
// from from at tempo, may go on: to its end, but six minutes at most.
func throughLimit(from, length time.Duration, tempo float64) time.Duration {
	d := time.Duration(float64(length-from)/tempo) + 5*time.Second
	return min(max(d, 10*time.Second), 6*time.Minute)
}

// hookLesson has the player ask the settings how a lesson goes, and tell
// the app as the song plays for the listener to sing along with it: to
// record them as they do, and play them back once it stops.
func (a *app) hookLesson() {
	a.pl.Guide = func() float64 { return a.settings.Guide }
	a.pl.Records = func() bool { return a.settings.AutoRecord }
	// With the teacher on, a line plays once and pauses: the teacher says
	// what comes next. Sung along with, it still plays as loud as Guide
	// says: it went silent, Guide made 0 for this.
	a.pl.Coached = func() bool { return a.teacher.on }
	a.pl.SingAlong = func(on bool) {
		t := &a.lesson.takes
		if !on {
			a.stopRecording()
			return
		}
		want := t.want
		t.want = false
		n, _ := a.pl.Line()
		lines, _ := a.pl.Lyrics()
		if !want && !a.settings.AutoRecord || n < 0 || n >= len(lines) {
			return
		}
		// Where the take starts, until the microphone says: as the song
		// starts playing for the listener to sing along with it.
		from := max(0, lines[n].At-player.LineLead)
		if a.pl.SingingThrough() {
			a.record(throughTake, throughLimit(lines[n].At, a.pl.Duration(), a.pl.Tempo()), from)
		} else {
			a.record(n, takeLimit(lines[n], a.pl.Tempo()), from)
		}
	}
}

// singTake plays the song softly from line n for the listener to sing
// along with it, recording them: that line alone, or on from it, sung
// through.
func (a *app) singTake(n int, through bool) {
	t := &a.lesson.takes
	a.stopTake()
	t.want = true
	ok := false
	if through {
		ok = a.pl.SingThrough(n)
	} else {
		ok = a.pl.SingLine(n)
	}
	if !ok {
		t.want = false
	}
}

// record starts recording the take key, sung from from in the song, for
// at most limit, with no take playing: the music plays on, for the
// listener to sing along.
func (a *app) record(key int, limit, from time.Duration) {
	t := &a.lesson.takes
	a.stopTake()
	a.stopRecording()
	sync := time.Duration(a.settings.Sync * float64(time.Second))
	tempo := a.pl.Tempo()
	rec := &recording{key: key, limit: int(limit.Seconds() * mic.Rate), take: &take{at: max(0, from+sync), tempo: tempo}}
	stop, err := a.microphone().listen(func(s []float32) {
		arrived := time.Now()
		rec.mu.Lock()
		if rec.full {
			rec.mu.Unlock()
			return
		}
		if len(rec.samples) == 0 {
			// The first sound came in as long ago as it lasts, the song
			// heard then where it plays now, less that long, at tempo:
			// where the take starts in it.
			heard := time.Duration(len(s)) * time.Second / mic.Rate
			a.post(func() {
				if a.pl.SingingAlong() {
					ago := time.Since(arrived) + heard
					rec.take.at = max(0, a.pl.Position()-time.Duration(float64(ago)*tempo)+sync)
				}
			})
		}
		rec.samples = append(rec.samples, s...)
		rec.level = mic.Level(s)
		rec.full = len(rec.samples) >= rec.limit
		full := rec.full
		rec.mu.Unlock()
		if full {
			a.post(func() {
				if t.rec == rec {
					a.stopRecording()
				}
			})
		} else {
			a.post(func() {}) // the meter moves
		}
	})
	if err != nil {
		t.err = "麦克风打不开：" + err.Error()
		return
	}
	rec.stop = stop
	t.rec, t.err = rec, ""
}

// stopRecording ends the take on its way, keeping it unless too short to
// be singing, and saving it as a file, then plays it back, unless the
// music plays on.
func (a *app) stopRecording() {
	t := &a.lesson.takes
	rec := t.rec
	if rec == nil {
		return
	}
	t.rec = nil
	rec.stop.Stop()
	rec.mu.Lock()
	samples := rec.samples
	rec.mu.Unlock()
	if len(samples) < mic.Rate*3/10 {
		t.err = "录得太短了，再试一次"
		return
	}
	if mic.Level(samples) == 0 {
		// How long it went and how loud it got, to tell a microphone that
		// gave nothing from one that heard a quiet voice.
		var peak float64
		for _, s := range samples {
			peak = max(peak, math.Abs(float64(s)))
		}
		loudest := "完全无声"
		if peak > 0 {
			loudest = fmt.Sprintf("最响 %.0f dB", 20*math.Log10(peak))
		}
		t.err = fmt.Sprintf("没录到声音（%.1f 秒，%s）：看看系统的声音设置里，默认的麦克风是否正确、没有静音", float64(len(samples))/mic.Rate, loudest)
	}
	if t.sung == nil {
		t.sung = map[int]*take{}
	}
	rec.take.samples = samples
	t.sung[rec.key] = rec.take
	if a.win != nil {
		song := ""
		if tr := a.pl.Current(); tr != nil {
			song = tr.Name
		}
		part := "整首"
		if rec.key != throughTake {
			lines, _ := a.pl.Lyrics()
			part = fmt.Sprintf("第%d句", lineNumber(lines, rec.key))
		}
		go saveTake(song, part, samples)
	}
	play := func() {
		// The teacher plays it back if it wants to.
		if !a.pl.Playing() && !a.teacher.on {
			a.playTake(rec.key)
		}
	}
	if rec.key == throughTake && rec.take.tempo == 1 && len(samples) >= alignLeast*mic.Rate {
		a.align(rec.take, play)
		return
	}
	play()
}

// alignLeast is the fewest seconds of a take sung through that lines up
// with the song clearly enough to learn the delay from.
const alignLeast = 30

// alignReach is how far either way of where the clock says a take starts
// it may line up with the song.
const alignReach = 1500 * time.Millisecond

// align lines tk up with the song, by where sound comes in, in the voice
// and in the song's own: where it matches clearly, it moves the take
// there, and keeps how far, as the delay of the speakers and microphone,
// for the takes after. Then it calls then.
func (a *app) align(tk *take, then func()) {
	t := &a.lesson.takes
	au, err := a.pl.OpenAudio()
	if err != nil {
		then()
		return
	}
	t.aligning = true
	samples, at := tk.samples, tk.at
	go func() {
		defer au.Close()
		rate, ch := au.SampleRate(), au.Channels()
		from := at - alignReach
		pad := 0
		if from < 0 {
			pad, from = int(-from.Seconds()*float64(rate)), 0
		}
		lag, ok := time.Duration(0), false
		if au.SetPosition(int64(from.Seconds()*float64(rate))) == nil {
			n := int((time.Duration(len(samples))*time.Second/mic.Rate + 2*alignReach).Seconds() * float64(rate))
			song := make([]float32, pad, n)
			buf := make([]float32, 4096*ch)
			var err error
			for len(song) < n && err == nil {
				var got int
				got, err = au.Read(buf)
				for i := 0; i+ch <= got && len(song) < n; i += ch {
					var v float32
					for c := range ch {
						v += buf[i+c]
					}
					song = append(song, v/float32(ch))
				}
			}
			// The song may end before the take and reach after it, sung to
			// its end: Align hears silence past it. Only a song that can no
			// longer be read, the track changed, matches nothing.
			if err == nil || err == io.EOF {
				lag, ok = mic.Align(samples, song, rate, alignReach)
			}
		}
		a.post(func() {
			t.aligning = false
			if ok {
				tk.at = max(0, tk.at+lag)
				a.settings.Sync = min(max(a.settings.Sync+lag.Seconds(), -alignReach.Seconds()), alignReach.Seconds())
				a.settings.save()
			}
			then()
		})
	}()
}

// nudgeSync moves the takes, and those to come, d later in the song, as
// the listener hears them line up, playing the one playing again.
func (a *app) nudgeSync(d time.Duration) {
	t := &a.lesson.takes
	a.settings.Sync += d.Seconds()
	a.settings.save()
	for _, tk := range t.sung {
		tk.at = max(0, tk.at+d)
	}
	if t.out != nil {
		a.playTake(t.key)
	}
}

// playTake plays back the take key, louder if sung quietly, with the
// song under it, softly, from where it was sung.
func (a *app) playTake(key int) {
	t := &a.lesson.takes
	tk, ok := t.sung[key]
	if !ok {
		return
	}
	a.stopTake()
	a.stopVocals()
	d := time.Duration(len(tk.samples)) * time.Second / mic.Rate
	if !a.pl.PlayUnder(tk.at, d, tk.tempo) && a.pl.Playing() {
		a.pl.TogglePlay()
	}
	t.out, t.key = audio.NewSound(mic.Rate, 1, mic.Normalize(tk.samples)).Play(), key
}

// playingThroughTake reports whether the take of the song sung through
// plays back, the song on under it, line after line.
func (a *app) playingThroughTake() bool {
	t := &a.lesson.takes
	return t.out != nil && t.key == throughTake && a.pl.PlayingUnder()
}

// stopTake stops a take playing back, and the song under it.
func (a *app) stopTake() {
	t := &a.lesson.takes
	if t.out != nil {
		t.out.Close()
		t.out = nil
		if a.pl.PlayingUnder() {
			a.pl.StopLine()
		}
	}
}

// forgetTakes drops the takes, as another song shows: they were of its
// lines.
func (a *app) forgetTakes() {
	a.stopRecording()
	a.stopTake()
	a.lesson.takes = takes{}
}

// checkTake ends the take on its way once it is as long as it may be,
// and lets go of the take played back once it has played.
func (a *app) checkTake() {
	t := &a.lesson.takes
	if rec := t.rec; rec != nil {
		rec.mu.Lock()
		full := rec.full
		rec.mu.Unlock()
		if full {
			a.stopRecording()
		}
	}
	if t.out != nil && !t.out.Playing() {
		t.out.Close()
		t.out = nil
	}
}

// takeButton plays back the take key, if there is one, saying how long
// it is: or stops it, playing.
func (a *app) takeButton(c *ui.Context, key int) {
	t := &a.lesson.takes
	tk, ok := t.sung[key]
	if !ok || t.rec != nil {
		return
	}
	playing := t.out != nil && t.key == key
	icon := iconPlay
	if playing {
		icon = iconPause
	}
	b := ui.ButtonBase(c.Key("play-take")).Height(32).PaddingX(12).Gap(6).Radius(999).AlignItems(ui.Center).FocusRing(false).
		Label("听我唱的").Tooltip("听我唱的，原曲小声伴着")
	bg := white.Alpha(0.1)
	if playing {
		bg = white.Alpha(0.2)
	} else if b.Hovered() {
		bg = white.Alpha(0.16)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond}))
	b.Children(func() {
		ui.Icon(c, icon).Size(11, 11).TextColor(white)
		ui.Text(c, clock(time.Duration(len(tk.samples))*time.Second/mic.Rate)).FontSize(12.5).FontWeight(600).FontFeatures("tnum").TextColor(white)
	})
	if b.Clicked() {
		a.teacherYield()
		if playing {
			a.stopTake()
		} else {
			a.playTake(key)
		}
	}
}

// takesDir is where takes are kept as WAV files.
func takesDir() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "takes"), nil
}

// saveTake keeps a take of song as a WAV file, named for when it was sung,
// the song, and the part of it sung: the line, or 整首 for the song sung
// through.
func saveTake(song, part string, samples []float32) {
	dir, err := takesDir()
	if err != nil || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	name := fmt.Sprintf("%s %s %s.wav", time.Now().Format("2006-01-02 150405"), fileSafe(song), part)
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return
	}
	defer f.Close()
	mic.WriteWAV(f, samples)
}

// fileSafe is name without what a file's name cannot hold.
func fileSafe(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, name)
}

// openTakesFolder shows the folder of takes in the system's file browser.
func openTakesFolder() error {
	dir, err := takesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := "xdg-open"
	switch runtime.GOOS {
	case "windows":
		name = "explorer"
	case "darwin":
		name = "open"
	}
	return exec.Command(name, dir).Start()
}
