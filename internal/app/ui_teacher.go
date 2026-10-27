package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// teacherGreen is the green of the teacher on.
var teacherGreen = ui.Hex("#46a758")

// teacherButton turns the AI teacher on and off: lit while on.
func (a *app) teacherButton(c *ui.Context) {
	t := &a.teacher
	b := ui.ButtonBase(c.Key("teacher")).Height(32).PaddingX(12).Gap(6).Radius(999).AlignItems(ui.Center).FocusRing(false).
		Label("AI 老师").Tooltip("AI 老师：说话上课，它放歌、听你唱、给你建议")
	bg := white.Alpha(0.1)
	switch {
	case t.on:
		bg = teacherGreen.Alpha(0.3)
	case b.Hovered():
		bg = white.Alpha(0.16)
	}
	b.Background(b.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond}))
	b.Children(func() {
		dot := white.Alpha(0.35)
		if t.on {
			dot = teacherGreen
		}
		ui.Box(c).Size(7, 7).Radius(3.5).Background(dot)
		ui.Text(c, "AI 老师").FontSize(12.5).FontWeight(600).TextColor(white).SingleLine()
	})
	if b.Clicked() {
		if t.on || t.ask {
			a.stopTeacher()
		} else {
			a.startTeacher()
		}
	}
}

// teacherKeyForm asks for the DashScope API key, to keep and connect with.
func (a *app) teacherKeyForm(c *ui.Context) {
	t := &a.teacher
	ui.Text(c, "粘贴阿里云百炼（DashScope）的 API Key。它存在系统的凭据管理器里，不写进任何文件。").FontSize(12.5).LineHeight(1.4).TextColor(white.Alpha(0.6)).MaxLines(2)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c.Key("api-key"), &t.key).Password().Placeholder("sk-…").Label("DashScope API Key").
			Grow(1).MinWidth(0).Height(34).PaddingX(12).Radius(10).Background(white.Alpha(0.08)).Border(1, white.Alpha(0.15)).FocusRing(false).
			FontSize(13).TextColor(white).AutoFocus()
		if in.Submitted() {
			a.pasteTeacherKey()
		}
		ok := pillButton(c.Key("connect"), nil, "连接", teacherGreen, white).Label("连接")
		if strings.TrimSpace(t.key) == "" {
			ok.Disabled(true).Opacity(0.5)
		}
		if ok.Clicked() {
			a.pasteTeacherKey()
		}
	})
}

// teacherPanel shows, over the dock, what the teacher does now and the
// last of what it and the listener said, or why it went wrong.
func (a *app) teacherPanel(c *ui.Context) {
	t := &a.teacher
	if !t.on && !t.ask && t.err == "" {
		return
	}
	state := "正在连接 AI 老师…"
	switch {
	case t.sess == nil:
	case a.teacherSpeaking() || t.saying:
		state = "老师在说"
	case t.answering || len(t.calls) > 0 && !a.pl.Playing():
		state = "老师在想"
	case a.pl.Playing() || a.lesson.takes.out != nil:
		state = "在放，老师在听歌"
	case a.lesson.takes.rec != nil:
		state = "在听你唱"
	default:
		state = "在听你说"
	}
	ui.Column(c.Key("teacher-panel")).Gap(6).Margin(6, 8, 0, 8).Padding(10, 14).Radius(14).Background(white.Alpha(0.06)).Children(func() {
		if t.on {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				dot := teacherGreen.Alpha(0.4)
				if t.sess != nil {
					dot = teacherGreen
				}
				ui.Box(c).Size(7, 7).Radius(3.5).Background(dot)
				ui.Text(c, state).FontSize(12).FontWeight(600).TextColor(white.Alpha(0.6)).SingleLine()
			})
		}
		if t.err != "" {
			ui.Text(c, t.err).FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#f5a524")).MaxLines(3)
		}
		switch {
		case t.ask:
			a.teacherKeyForm(c)
		case !t.on && t.err != "":
			// It went wrong, perhaps for the key.
			change := ui.ButtonBase(c.Key("change-key")).Row().AlignSelf(ui.Start).FocusRing(false).Label("换一个 API Key")
			change.Children(func() {
				fg := white.Alpha(0.6)
				if change.Hovered() {
					fg = white
				}
				ui.Text(c, "换一个 API Key").FontSize(12.5).FontWeight(600).TextColor(fg)
			})
			if change.Clicked() {
				t.ask, t.err = true, ""
			}
		}
		// The last few things said, the newest last.
		said := t.said[max(0, len(t.said)-3):]
		for i, u := range said {
			who, color := "你：", white.Alpha(0.55)
			if u.teacher {
				who, color = "老师：", white.Alpha(0.92)
			}
			ui.Text(c.Key(len(t.said)-len(said)+i), who+u.text).FontSize(13.5).LineHeight(1.35).TextColor(color).MaxLines(3)
		}
		if t.hearing != "" {
			ui.Text(c, "你："+t.hearing).FontSize(13.5).LineHeight(1.35).TextColor(white.Alpha(0.4)).MaxLines(2)
		}
	})
	if t.on {
		// What it does now changes as its voice plays out.
		c.After(200 * time.Millisecond)
	}
}
