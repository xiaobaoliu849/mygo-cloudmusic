package app

import (
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/teacher"
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
		ui.Box(c).Size(7, 7).Radius(3.5).Background(a.teacherDot())
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

// teacherDot is the color of the dot saying how the teacher is: grey off,
// amber connecting, green connected.
func (a *app) teacherDot() ui.Color {
	t := &a.teacher
	switch {
	case t.sess != nil:
		return teacherGreen
	case t.on:
		return ui.Hex("#f5a524")
	}
	return white.Alpha(0.35)
}

// teacherKeyForm asks for the API key of the teacher's provider, to keep
// and connect with.
func (a *app) teacherKeyForm(c *ui.Context) {
	t := &a.teacher
	who, placeholder, label := "阿里云百炼（DashScope）", "sk-…", "DashScope API Key"
	if a.settings.TeacherProvider == teacher.Gemini {
		who, placeholder, label = "Google AI Studio（Gemini）", "AIza…", "Gemini API Key"
	}
	ui.Text(c, "粘贴"+who+"的 API Key。它存在系统的凭据管理器里，不写进任何文件。").FontSize(12.5).LineHeight(1.4).TextColor(white.Alpha(0.6)).MaxLines(2)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c.Key("api-key"), &t.key).Password().Placeholder(placeholder).Label(label).
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
	// What the teacher does now; listening, the microphone's meter shows
	// it hears the listener.
	// Whose model it is shows, as the lesson's settings choose it.
	who := "通义"
	if a.settings.TeacherProvider == teacher.Gemini {
		who = "Gemini"
	}
	state, listening := "正在连接 AI 老师（"+who+"）…", false
	switch {
	case t.sess == nil:
	case a.teacherSpeaking() || t.saying:
		state = "已连上" + who + " · 老师在说"
	case t.answering || len(t.calls) > 0 && !a.pl.Playing():
		state = "已连上" + who + " · 老师在想"
	case a.pl.Playing() || a.lesson.takes.out != nil:
		state = "已连上" + who + " · 在放，放完老师再听你说"
	case a.lesson.takes.rec != nil:
		state, listening = "已连上"+who+" · 在听你唱", true
	default:
		state, listening = "已连上"+who+" · 在听你说，直接说话就行", true
	}
	ui.Column(c.Key("teacher-panel")).Gap(6).Margin(6, 8, 0, 8).Padding(10, 14).Radius(14).Background(white.Alpha(0.06)).Children(func() {
		if t.on {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(7, 7).Radius(3.5).Background(a.teacherDot())
				ui.Text(c, state).FontSize(12).FontWeight(600).TextColor(white.Alpha(0.6)).SingleLine()
				if listening {
					level := float32(0)
					if t.level != nil {
						level = math.Float32frombits(t.level.Load())
					}
					ui.Box(c).Width(60).Height(4).Radius(2).Background(white.Alpha(0.12)).Children(func() {
						ui.Box(c).Width(60 * level).Height(4).Radius(2).Background(teacherGreen)
					})
				}
			})
		}
		if t.err != "" {
			ui.Text(c, t.err).FontSize(12.5).FontWeight(600).TextColor(ui.Hex("#f5a524")).MaxLines(3)
		}
		switch {
		case t.ask:
			a.teacherKeyForm(c)
		case !t.on && strings.HasPrefix(t.err, teacherLost):
			// The connection was lost, not the key: it connects again.
			again := ui.ButtonBase(c.Key("reconnect")).Row().AlignSelf(ui.Start).FocusRing(false).Label("重新连上")
			again.Children(func() {
				fg := white.Alpha(0.6)
				if again.Hovered() {
					fg = white
				}
				ui.Text(c, "重新连上").FontSize(12.5).FontWeight(600).TextColor(fg)
			})
			if again.Clicked() {
				t.err = ""
				a.startTeacher()
			}
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
			ui.Text(c.Key(len(t.said)-len(said)+i), who+strings.TrimSpace(u.text)).FontSize(13.5).LineHeight(1.35).TextColor(color).MaxLines(3)
		}
		if t.hearing != "" {
			ui.Text(c, "你："+t.hearing).FontSize(13.5).LineHeight(1.35).TextColor(white.Alpha(0.4)).MaxLines(2)
		}
	})
	if t.on {
		// What it does now changes as its voice plays out, and the meter
		// moves.
		c.After(100 * time.Millisecond)
	}
}
