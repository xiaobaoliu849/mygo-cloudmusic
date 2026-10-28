package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/mic"
	"netease-music-mygo/internal/netease"
	"netease-music-mygo/internal/player"
	"netease-music-mygo/internal/teacher"
)

// lessonState is the lesson pane: the listener learns a song a line at a
// time, hearing a line alone, then singing it, or sings it through.
type lessonState struct {
	through bool // sing the song through, not a line at a time
	loop    bool // a line chosen plays over and over, not once
	more    bool // the lesson's settings show
	pointed int  // the line the teacher points to, plus one; 0 for none
	scroll  ui.ScrollState
	track   int64 // the track the pane last showed
	at      int   // the line it last scrolled to
	takes   takes
}

// recordRed is the red of recording.
var recordRed = ui.Hex("#e5484d")

// lessonPane fills the pane with the song's lines, the one to start from
// lit, over a dock of the lesson's controls: how to learn, a line at a
// time or the song through; one button that starts and stops; and what
// the listener sang, to hear it. A click on a line starts from it.
// Credits, as 作词, are not sung, so not listed.
func (a *app) lessonPane(c *ui.Context, tr *netease.Track) {
	pl := a.pl
	ls := &a.lesson
	lines, loaded := pl.Lyrics()
	if len(lines) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			if !loaded {
				lyricsSkeleton(c, npShimmer(c))
				return
			}
			ui.Text(c, "这首歌没有歌词，换一首来跟唱吧").FontSize(20).FontWeight(700).TextColor(white.Alpha(0.4))
		})
		return
	}
	if ls.track != tr.ID {
		ls.track, ls.at, ls.scroll, ls.pointed = tr.ID, -2, ui.ScrollState{}, 0
		a.forgetTakes()
	}
	a.checkTake()
	pos := pl.Position()
	sung := lyrics.Index(lines, pos)
	if pl.Playing() && sung+1 < len(lines) {
		// Light the next line as it begins.
		c.After(lines[sung+1].At - pos + 20*time.Millisecond)
	}
	// The line to start from: the one the teacher points to, else the one
	// chosen, else the one sung now.
	at := a.lessonAt(lines)
	// The line to keep in view: the one sung as the song plays on, sung
	// through or under the take of it, else the one to start from; not
	// the one before, which the song plays in over.
	focus := at
	if pl.SingingThrough() || a.playingThroughTake() {
		focus = max(at, sung)
	}

	scroll := ui.Scroll(c.Key("lesson")).Grow(1).TrackScroll(&ls.scroll).Padding(4, 8, 24, 0)
	var active ui.Element
	scroll.Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			num := 0 // the lines sung so far: the credits are not numbered
			for i, l := range lines {
				if lyrics.IsCredit(l.Text) {
					continue
				}
				num++
				number := num
				row := ui.ButtonBase(c.Key(i)).Gap(12).Padding(10, 14).Radius(14).AlignItems(ui.Start).FocusRing(false).Label(l.Text)
				bg := ui.Transparent
				switch {
				case i == focus:
					bg = white.Alpha(0.12)
				case row.Hovered():
					bg = white.Alpha(0.05)
				}
				row.Background(row.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond}))
				alpha := float32(0.4)
				if i == focus || i == sung && pl.Playing() {
					alpha = 1
				} else if row.Hovered() {
					alpha = 0.6
				}
				row.Children(func() {
					ui.Text(c, fmt.Sprint(number)).Width(22).FontSize(12).FontFeatures("tnum").Margin(8, 0, 0, 0).TextColor(white.Alpha(0.3))
					ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
						ui.Text(c, l.Text).FontSize(22).FontWeight(700).LineHeight(1.3).TextColor(white.Alpha(alpha))
						if r := a.romaji(l); r != "" {
							ui.Text(c, r).FontSize(14.5).FontWeight(500).TextColor(white.Alpha(alpha * 0.8))
						}
						if tl := a.translation(l); tl != "" {
							ui.Text(c, tl).FontSize(13.5).FontWeight(500).TextColor(white.Alpha(alpha * 0.6))
						}
					})
				})
				if row.Clicked() {
					a.startLesson(i, false)
				}
				if i == focus {
					active = row
				}
			}
		})
	})

	a.teacherPanel(c)
	a.lessonStatus(c, at, sung)
	a.lessonDock(c, lines, at)

	// Bring the line in focus into view, a third of the way down, once it
	// changes.
	if focus != ls.at && active.Valid() {
		box, lb := scroll.Bounds(), active.LayoutBounds()
		if lb.H == 0 || box.H == 0 {
			c.AnimationFrame() // not laid out yet: the next frame scrolls
			return
		}
		ls.at = focus
		target := lb.Y - box.Y + ls.scroll.Y - box.H*0.3
		ls.scroll.ScrollTo(0, min(max(0, target), ls.scroll.MaxY), ui.Animation{Spring: ui.SpringOptions{Duration: 500 * time.Millisecond}})
	}
}

// romaji is line l's romaji, unless the listener hides it.
func (a *app) romaji(l lyrics.Line) string {
	if a.settings.HideRomaji {
		return ""
	}
	return l.Romaji
}

// translation is line l's translation, unless the listener hides it.
func (a *app) translation(l lyrics.Line) string {
	if a.settings.HideTranslation {
		return ""
	}
	return l.Translation
}

// lineNumber is line n's number as the lesson shows it: the lines sung
// counted from 1, the credits, as 作词, left out, as the lesson leaves
// them out; 0 for one of the credits.
func lineNumber(lines []lyrics.Line, n int) int {
	if n < 0 || n >= len(lines) || lyrics.IsCredit(lines[n].Text) {
		return 0
	}
	num := 0
	for _, l := range lines[:n+1] {
		if !lyrics.IsCredit(l.Text) {
			num++
		}
	}
	return num
}

// numberedLine is the line numbered num, as lineNumber numbers them,
// reporting false for none.
func numberedLine(lines []lyrics.Line, num int) (int, bool) {
	for n, l := range lines {
		if lyrics.IsCredit(l.Text) {
			continue
		}
		if num--; num == 0 {
			return n, true
		}
	}
	return 0, false
}

// startLesson starts from line n: a line at a time, it plays the line to
// hear, then to sing along with, or, again, if heard already, goes
// straight to singing it; sung through, the song plays on from it.
func (a *app) startLesson(n int, again bool) {
	ls := &a.lesson
	ls.pointed = 0
	// What went wrong with a take before is told no more.
	ls.takes.err = ""
	a.teacherYield()
	a.stopRecording()
	a.stopTake()
	switch {
	case ls.through:
		a.singTake(n, true)
	case again:
		a.singTake(n, false)
	case ls.loop:
		a.pl.LoopLine(n, 0)
	default:
		a.pl.PlayLine(n)
	}
	a.stopVocals()
}

// lessonBusy reports whether the lesson plays or records something the
// big button stops.
func (a *app) lessonBusy() bool {
	_, alone := a.pl.Line()
	t := &a.lesson.takes
	return alone || a.pl.SingingThrough() || t.rec != nil || t.out != nil || a.vocalsPlaying()
}

// stopLesson stops what the lesson plays or records, the take recorded
// playing back.
func (a *app) stopLesson() {
	t := &a.lesson.takes
	a.teacherYield()
	switch {
	case a.vocalsPlaying():
		a.stopVocals()
	case t.out != nil:
		a.stopTake()
	case a.pl.SingingAlong() || a.pl.SingingThrough():
		// Its take stops with it, and plays back.
		a.pl.StopLine()
	case t.rec != nil:
		a.stopRecording()
	default:
		a.pl.StopLine()
	}
}

// lessonStatus says, in a line over the dock, where the lesson is and
// what the listener does now: or what went wrong.
func (a *app) lessonStatus(c *ui.Context, at, sung int) {
	pl := a.pl
	t := &a.lesson.takes
	chosen, alone := pl.Line()
	lines, _ := pl.Lyrics()
	line := fmt.Sprintf("第 %d 句", lineNumber(lines, at))
	if pl.SingingThrough() {
		line = fmt.Sprintf("第 %d 句", lineNumber(lines, max(at, sung)))
	}
	text, color := "", white.Alpha(0.55)
	switch {
	case t.err != "":
		text, color = t.err, ui.Hex("#f5a524")
	case t.aligning:
		text = "正在对齐你唱的和原曲…"
	case t.rec != nil && a.teacher.on:
		// The teacher hears them as they sing: nothing kept to show.
		text, color = line+" · 跟着唱 · 老师在听", white.Alpha(0.85)
	case t.rec != nil:
		t.rec.mu.Lock()
		got := len(t.rec.samples)
		t.rec.mu.Unlock()
		text, color = line+" · 跟着唱 · 录音中 "+clock(time.Duration(got)*time.Second/mic.Rate), white.Alpha(0.85)
	case a.vocals.err != "":
		text, color = a.vocals.err, ui.Hex("#f5a524")
	case a.vocals.want != 0:
		text = line + " · 正在分离原唱的人声…"
	case a.vocals.out != nil:
		text = line + " · 只听原唱的人声"
	case t.out != nil:
		text = "在放你唱的，原曲在下面小声伴着"
	case pl.SingingThrough(), pl.SingingAlong():
		text = line + " · 跟着唱"
	case alone:
		text = line + " · 先听一遍"
	case a.lesson.through:
		text = "从" + line + "一直唱下去：按 ●，或点任意一句"
	case chosen >= 0:
		text = line + " · 按 ● 再唱一遍，或点下一句"
	default:
		text = "点一句歌词，先听再跟着唱"
	}
	ui.Text(c, text).FontSize(12.5).FontWeight(600).TextColor(color).TextAlign(ui.Center).AlignSelf(ui.Stretch).SingleLine().Padding(6, 12, 2, 12)
}

// lessonDock is the lesson's controls along the pane's foot: how to learn
// at its left, the button that starts and stops in the middle, between
// the line before and after, and what was sung, and the settings, at its
// right.
func (a *app) lessonDock(c *ui.Context, lines []lyrics.Line, at int) {
	ls := &a.lesson
	ui.Row(c.Key("dock")).Height(76).AlignItems(ui.Center).Padding(0, 8, 8, 8).Children(func() {
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			a.teacherButton(c)
			choice(c, "学法", []choiceOption{{"逐句", 0}, {"整首", 1}}, boolValue(ls.through), func(v float64) {
				if through := v == 1; through != ls.through {
					a.stopLesson()
					a.stopTake()
					ls.through = through
				}
			})
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if !ls.through {
				// The line sung before, or after: not one of the credits.
				if iconButton(c.Key("prev-line"), iconBack, 18, white.Alpha(0.75), "上一句").Tooltip("上一句").Clicked() {
					if n, ok := stepLine(lines, at, -1); ok {
						a.startLesson(n, false)
					}
				}
			}
			a.bigButton(c, at)
			if !ls.through {
				if iconButton(c.Key("next-line"), iconForward, 18, white.Alpha(0.75), "下一句").Tooltip("下一句").Clicked() {
					if n, ok := stepLine(lines, at, 1); ok {
						a.startLesson(n, false)
					}
				}
			}
		})
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(2).Justify(ui.End).AlignItems(ui.Center).Children(func() {
			if !ls.through {
				fg := white.Alpha(0.45)
				if ls.loop {
					fg = white
				}
				loop := iconButton(c.Key("loop-line"), iconRepeat, 16, fg, "单句循环").Tooltip("单句循环：一句反复听、反复唱")
				if ls.loop {
					loop.Background(white.Alpha(0.14))
				}
				if loop.Clicked() {
					ls.loop = !ls.loop
				}
			}
			if !ls.through && a.vocalsReady() {
				a.vocalsButton(c, at)
			}
			key := at
			if ls.through {
				key = throughTake
			}
			a.takeButton(c, key)
			a.settingsButton(c)
		})
	})
}

// bigButton starts from line at, red as recording does, green with the
// teacher, who listens rather than records, or, while the lesson plays or
// records, stops it, a square on white: a ring round it swelling with the
// voice the microphone hears.
func (a *app) bigButton(c *ui.Context, at int) {
	t := &a.lesson.takes
	busy := a.lessonBusy()
	accent := recordRed
	if a.teacher.on {
		accent = teacherGreen
	}
	chosen, _ := a.pl.Line()
	label, tip := "开始唱", "先听这一句，再跟着唱"
	switch {
	case busy:
		label, tip = "停止", "停止"
	case a.lesson.through:
		lines, _ := a.pl.Lyrics()
		tip = fmt.Sprintf("从第 %d 句一直唱下去", lineNumber(lines, at))
	case chosen == at:
		tip = "再唱一遍这一句"
	}
	const size = 54
	ui.Box(c.Key("big")).Size(size+16, size+16).Children(func() {
		// Both placed, the button after the ring, over it: what is placed
		// paints over what flows.
		if t.rec != nil {
			t.rec.mu.Lock()
			level := t.rec.level
			t.rec.mu.Unlock()
			s := 0.8 + 0.45*level
			ui.Box(c).Absolute().Top(8).Left(8).Size(size, size).Radius(size/2).Background(accent.Alpha(0.35)).Scale(s, s).PassThrough()
		}
		b := ui.ButtonBase(c.Key("big-button")).Absolute().Top(8).Left(8).Size(size, size).Radius(size / 2).Center().FocusRing(false).Label(label).Tooltip(tip)
		bg := accent
		if busy {
			bg = white.Alpha(0.92)
		}
		if b.Hovered() {
			bg = bg.Mix(white, 0.12)
		}
		b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond})).Shadow(0, 6, 18, -4, ui.RGBA(0, 0, 0, 0.35))
		pose := ui.Pose{}
		if b.Pressed() {
			pose.Scale = 0.92
		}
		b.AnimatePose(pose, ui.Snappy)
		b.Children(func() {
			if busy {
				ui.Box(c).Size(18, 18).Radius(4).Background(ui.Hex("#1c1c1f"))
			} else {
				ui.Icon(c, iconPodcast).Size(22, 22).TextColor(white)
			}
		})
		if b.Clicked() {
			if busy {
				a.stopLesson()
			} else {
				a.startLesson(at, chosen == at && !a.lesson.through)
			}
		}
	})
	if t.rec != nil || t.out != nil {
		c.After(50 * time.Millisecond)
	}
}

// settingsButton opens the lesson's settings in a card over the dock.
func (a *app) settingsButton(c *ui.Context) {
	ls := &a.lesson
	fg := white.Alpha(0.6)
	if ls.more {
		fg = white
	}
	b := iconButton(c.Key("settings"), iconSettings, 17, fg, "跟唱设置").Tooltip("跟唱设置")
	if b.Clicked() {
		ls.more = !ls.more
	}
	pop := popoverColors(true)
	ui.PopoverBase(c.Key("lesson-settings"), b, &ls.more, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorTopRight, ui.AnchorBottomRight).Margin(0, 0, 8, 0).Width(320).Padding(14, 16, 12, 16).
			Radius(14).Background(pop.fill).Border(0.5, pop.edge).Shadow(0, 12, 32, -6, ui.RGBA(0, 0, 0, 0.45)).
			Gap(12).TextColor(white).Label("跟唱设置")
		setting := func(title, hint string, control func()) {
			ui.Row(c.Key(title)).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine()
					if hint != "" {
						ui.Text(c, hint).FontSize(11).TextColor(white.Alpha(0.45)).SingleLine()
					}
				})
				control()
			})
		}
		setting("原唱音量", "跟着唱时，原曲放多大声", func() {
			choice(c, "原唱音量", guideOptions, a.settings.Guide, func(v float64) {
				a.settings.Guide = v
				a.pl.GuideChanged()
				a.settings.save()
			})
		})
		setting("速度", "慢下来，音调不变", func() {
			choice(c, "速度", tempoOptions, a.pl.Tempo(), func(v float64) {
				a.settings.Tempo = v
				a.pl.SetTempo(v)
				a.settings.save()
			})
		})
		setting("自动录音", "自己练时跟着唱，把你录下来；AI 老师只听不存", func() {
			choice(c, "自动录音", []choiceOption{{"开", 1}, {"关", 0}}, boolValue(a.settings.AutoRecord), func(v float64) {
				a.settings.AutoRecord = v == 1
				a.settings.save()
			})
		})
		setting("AI 老师", "用谁的模型，各要各的 API Key", func() {
			cur := boolValue(a.settings.TeacherProvider == teacher.Gemini)
			choice(c, "AI 老师模型", []choiceOption{{"通义", 0}, {"Gemini", 1}}, cur, func(v float64) {
				a.settings.TeacherProvider = ""
				if v == 1 {
					a.settings.TeacherProvider = teacher.Gemini
				}
				a.settings.save()
				// On, or asking for a key, it starts over with the other.
				if a.teacher.on || a.teacher.ask {
					a.startTeacher()
				}
			})
		})
		setting("老师说", "AI 老师用什么语言和你说话", func() {
			choice(c, "老师说", []choiceOption{{"中文", 0}, {"English", 1}}, boolValue(a.settings.TeacherEnglish), func(v float64) {
				a.settings.TeacherEnglish = v == 1
				a.settings.save()
				// On, it starts over speaking the other.
				if a.teacher.on {
					a.startTeacher()
				}
			})
		})
		setting("戴耳机", "戴着耳机，老师说话、放歌时也能开口打断（Gemini）", func() {
			choice(c, "戴耳机", []choiceOption{{"戴着", 1}, {"没戴", 0}}, boolValue(a.settings.Headphones), func(v float64) {
				a.settings.Headphones = v == 1
				a.settings.save()
			})
		})
		setting("罗马音", "歌词下面标出怎么念", func() {
			choice(c, "罗马音", []choiceOption{{"开", 1}, {"关", 0}}, boolValue(!a.settings.HideRomaji), func(v float64) {
				a.settings.HideRomaji = v == 0
				a.settings.save()
			})
		})
		setting("翻译", "先学发音时可以关掉", func() {
			choice(c, "翻译", []choiceOption{{"开", 1}, {"关", 0}}, boolValue(!a.settings.HideTranslation), func(v float64) {
				a.settings.HideTranslation = v == 0
				a.settings.save()
			})
		})
		setting("人声对齐","回放时人声偏晚就按 −", func() {
			ui.Row(c).Gap(2).AlignItems(ui.Center).Padding(2).Radius(9).Background(white.Alpha(0.08)).Children(func() {
				if stepButton(c, "−", "人声早一点").Clicked() {
					a.nudgeSync(-50 * time.Millisecond)
				}
				ui.Text(c, fmt.Sprintf("%+.2fs", a.settings.Sync)).Width(54).TextAlign(ui.Center).FontSize(12).FontFeatures("tnum").TextColor(white.Alpha(0.8))
				if stepButton(c, "+", "人声晚一点").Clicked() {
					a.nudgeSync(50 * time.Millisecond)
				}
			})
		})
		ui.Box(c).Height(0.5).AlignSelf(ui.Stretch).Background(white.Alpha(0.1))
		folder := ui.ButtonBase(c.Key("takes-folder")).Row().AlignItems(ui.Center).Padding(2, 0).FocusRing(false).Label("打开录音文件夹")
		folder.Children(func() {
			fg := white.Alpha(0.6)
			if folder.Hovered() {
				fg = white
			}
			ui.Text(c, "打开录音文件夹").FontSize(12.5).FontWeight(600).TextColor(fg)
			ui.Spacer(c)
			ui.Icon(c, iconForward).Size(13, 13).TextColor(fg)
		})
		if folder.Clicked() {
			if err := openTakesFolder(); err != nil {
				a.lesson.takes.err = "打不开录音文件夹：" + err.Error()
			}
		}
	})
}

// stepButton is one of the small buttons either side of a value, that
// step it down or up.
func stepButton(c *ui.Context, text, label string) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Size(28, 24).Radius(7).Center().FocusRing(false).Label(label).Tooltip(label)
	if b.Hovered() {
		b.Background(white.Alpha(0.12))
	}
	b.Children(func() { ui.Text(c, text).FontSize(15).FontWeight(600).TextColor(white.Alpha(0.85)) })
	return b
}

// How loud the song plays for the listener to sing along with it.
const (
	guideSoft   = 0.2
	guideMedium = 0.45
	guideLoud   = 1
)

var (
	guideOptions = []choiceOption{{"不放", 0}, {"小声", guideSoft}, {"中等", guideMedium}, {"大声", guideLoud}}
	tempoOptions = []choiceOption{{"原速", 1}, {"0.9×", player.TempoSlower}, {"0.75×", player.TempoSlow}}
)

// A choiceOption is one of a choice's values, and what it says.
type choiceOption struct {
	label string
	v     float64
}

// boolValue is a choice's value for on or off.
func boolValue(on bool) float64 {
	if on {
		return 1
	}
	return 0
}

// choice is a choice of one of opts, as a row of segments in a well:
// the one at cur lit. A click on another sets it. Its options are named
// for assistive technology after title.
func choice(c *ui.Context, title string, opts []choiceOption, cur float64, set func(float64)) {
	ui.Row(c.Key(title)).Gap(2).Padding(2).Radius(9).Background(white.Alpha(0.08)).Children(func() {
		for _, o := range opts {
			on := cur == o.v
			b := ui.ButtonBase(c.Key(o.label)).Padding(4, 10).Radius(7).FocusRing(false).Label(title + o.label)
			bg, fg := ui.Transparent, white.Alpha(0.55)
			switch {
			case on:
				bg, fg = white.Alpha(0.2), white
			case b.Hovered():
				fg = white.Alpha(0.85)
			}
			b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond}))
			b.Children(func() { ui.Text(c, o.label).FontSize(12.5).FontWeight(600).TextColor(fg).SingleLine() })
			if b.Clicked() && !on {
				set(o.v)
			}
		}
	})
}
