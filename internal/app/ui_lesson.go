package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/lyrics"
	"netease-music-mygo/internal/netease"
	"netease-music-mygo/internal/player"
)

// lessonState is the lesson pane: the listener learns a song a line at a
// time, hearing a line alone, then singing it.
type lessonState struct {
	loop   bool // a line chosen plays over and over, not once
	scroll ui.ScrollState
	track  int64 // the track the pane last showed
	at     int   // the line it last scrolled to
}

// lessonPane lists the lines of the song, numbered: a click on one plays
// it alone, pausing at its end for the listener to sing it, or looping
// it. Credits, as 作词, are not sung, so not listed.
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
		ls.track, ls.at, ls.scroll = tr.ID, -2, ui.ScrollState{}
	}
	chosen, alone := pl.Line()
	pos := pl.Position()
	sung := lyrics.Index(lines, pos)
	if pl.Playing() && sung+1 < len(lines) {
		// Light the next line as it begins.
		c.After(lines[sung+1].At - pos + 20*time.Millisecond)
	}
	play := func(n int) {
		if ls.loop {
			pl.LoopLine(n, 0)
		} else {
			pl.PlayLine(n)
		}
	}

	ui.Column(c).Gap(4).Padding(0, 8, 14, 12).Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			// The line chosen, else the one sung now.
			again := chosen
			if again < 0 {
				again = max(0, sung)
			}
			if iconButton(c.Key("prev-line"), iconBack, 20, white.Alpha(0.8), "上一句").Tooltip("上一句").Clicked() {
				play(max(0, again-1))
			}
			if iconButton(c.Key("again"), iconReload, 20, white.Alpha(0.8), "再听一遍").Tooltip("再听一遍").Clicked() {
				play(again)
			}
			if iconButton(c.Key("next-line"), iconForward, 20, white.Alpha(0.8), "下一句").Tooltip("下一句").Clicked() {
				if again+1 < len(lines) {
					play(again + 1)
				}
			}
			loopFg := white.Alpha(0.45)
			if ls.loop {
				loopFg = white
			}
			loop := iconButton(c.Key("loop-line"), iconRepeat, 18, loopFg, "单句循环").Tooltip("单句循环")
			if ls.loop {
				loop.Background(white.Alpha(0.14))
			}
			if loop.Clicked() {
				ls.loop = !ls.loop
				if chosen >= 0 && alone {
					play(chosen)
				}
			}
			ui.Spacer(c)
			status := "点一句歌词，听完跟着唱"
			switch {
			case pl.SingingAlong():
				status = fmt.Sprintf("第 %d / %d 句 · 跟着原唱唱", chosen+1, len(lines))
			case chosen >= 0 && alone:
				status = fmt.Sprintf("第 %d / %d 句 · 正在播放", chosen+1, len(lines))
			case chosen >= 0:
				status = fmt.Sprintf("第 %d / %d 句 · 轮到你唱了", chosen+1, len(lines))
			}
			ui.Text(c, status).FontSize(12.5).FontWeight(600).TextColor(white.Alpha(0.6))
		})
		ui.Row(c).Gap(20).Padding(0, 0, 0, 8).Children(func() {
			a.choice(c, "跟唱时原唱", []choiceOption{{"不放", 0}, {"小声", guideSoft}, {"中等", guideMedium}}, a.settings.Guide, func(v float64) {
				a.settings.Guide = v
				a.pl.GuideChanged()
			})
			a.choice(c, "速度", []choiceOption{{"原速", 1}, {"0.9×", player.TempoSlower}, {"0.75×", player.TempoSlow}}, pl.Tempo(), func(v float64) {
				a.settings.Tempo = v
				pl.SetTempo(v)
			})
		})
	})

	scroll := ui.Scroll(c.Key("lesson")).Grow(1).TrackScroll(&ls.scroll).Padding(0, 8, 120, 0)
	var active ui.Element
	scroll.Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			for i, l := range lines {
				if lyrics.IsCredit(l.Text) {
					continue
				}
				row := ui.ButtonBase(c.Key(i)).Gap(14).Padding(10, 12).Radius(12).AlignItems(ui.Start).FocusRing(false).Label(l.Text)
				bg := ui.Transparent
				switch {
				case i == chosen:
					bg = white.Alpha(0.14)
				case row.Hovered():
					bg = white.Alpha(0.06)
				}
				row.Background(row.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
				alpha := float32(0.45)
				if i == sung && pl.Playing() || i == chosen {
					alpha = 1
				}
				row.Children(func() {
					ui.Text(c, fmt.Sprint(i+1)).Width(26).FontSize(13).FontFeatures("tnum").Margin(7, 0, 0, 0).TextColor(white.Alpha(0.35))
					ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
						ui.Text(c, l.Text).FontSize(22).FontWeight(700).LineHeight(1.3).TextColor(white.Alpha(alpha))
						if l.Translation != "" {
							ui.Text(c, l.Translation).FontSize(14).FontWeight(600).TextColor(white.Alpha(alpha * 0.65))
						}
					})
				})
				if row.Clicked() {
					play(i)
				}
				if i == chosen {
					active = row
				}
			}
		})
	})
	// Bring the line chosen into view, a third of the way down, once it
	// changes.
	if chosen != ls.at && active.Valid() {
		box, lb := scroll.Bounds(), active.LayoutBounds()
		if lb.H == 0 || box.H == 0 {
			c.AnimationFrame() // not laid out yet: the next frame scrolls
			return
		}
		ls.at = chosen
		target := lb.Y - box.Y + ls.scroll.Y - box.H*0.3
		ls.scroll.ScrollTo(0, min(max(0, target), ls.scroll.MaxY), ui.Animation{Spring: ui.SpringOptions{Duration: 500 * time.Millisecond}})
	}
}

// How loud a line plays the second time, to sing along with it.
const (
	guideSoft   = 0.2
	guideMedium = 0.45
)

// A choiceOption is one of a choice's values, and what it says.
type choiceOption struct {
	label string
	v     float64
}

// choice is a row of pills, titled, for one of opts: the one at cur lit.
// A click on another sets it, and keeps the settings.
func (a *app) choice(c *ui.Context, title string, opts []choiceOption, cur float64, set func(float64)) {
	ui.Row(c.Key(title)).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12.5).FontWeight(600).TextColor(white.Alpha(0.5)).Margin(0, 6, 0, 0)
		for _, o := range opts {
			on := cur == o.v
			b := ui.ButtonBase(c.Key(o.label)).Padding(4, 12).Radius(999).FocusRing(false).Label(title + o.label)
			bg, fg := ui.Transparent, white.Alpha(0.55)
			if on {
				bg, fg = white.Alpha(0.14), white
			} else if b.Hovered() {
				bg = white.Alpha(0.06)
			}
			b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
			b.Children(func() { ui.Text(c, o.label).FontSize(12.5).FontWeight(600).TextColor(fg) })
			if b.Clicked() && !on {
				set(o.v)
				a.settings.save()
			}
		}
	})
}
