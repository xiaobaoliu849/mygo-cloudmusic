package app

import (
	"github.com/egoist/mygo/ui"

	"netease-music-mygo/internal/speech"
)

// vocalsSettings are the settings' rows for the model that separates the
// singing, for a lesson to play a line's singing alone.
func (a *app) vocalsSettings(row func(label, desc string, control func()), c *ui.Context) {
	m := &a.vocals.model
	var desc string
	switch {
	case m.downloading:
		desc = "正在下载 " + megabytes(m.done) + " / " + megabytes(m.total)
	case m.err != "":
		desc = m.err
	case a.vocalsReady():
		desc = "UVR MDX-Net，已下载，占用 " + megabytes(speech.VocalsSize()) + "；跟唱时可以只听原唱的人声"
	default:
		desc = "UVR MDX-Net，约 " + megabytes(speech.VocalsSize()) + "，下载后跟唱时可以去掉伴奏、只听原唱的人声，在本机分离"
	}
	row("人声分离模型", desc, func() {
		switch {
		case m.downloading:
			if ui.Button(c.Key("vocals-model"), "取消").Clicked() {
				a.cancelVocalsDownload()
			}
		case a.vocalsReady():
			if ui.Button(c.Key("vocals-model"), "删除").Clicked() {
				a.deleteVocals()
			}
		default:
			if ui.Button(c.Key("vocals-model"), "下载").Clicked() {
				a.startVocalsDownload()
			}
		}
	})
}

// vocalsButton plays line at's singing alone, the singer's voice without
// the band, or stops it.
func (a *app) vocalsButton(c *ui.Context, at int) {
	playing := a.vocalsPlaying()
	fg := white.Alpha(0.45)
	if playing {
		fg = white
	}
	b := iconButton(c.Key("vocals"), iconNote, 16, fg, "只听人声").Tooltip("只听原唱的人声，去掉伴奏")
	if playing {
		b.Background(white.Alpha(0.14))
	}
	if b.Clicked() {
		a.teacherYield()
		if playing {
			a.stopVocals()
			return
		}
		a.stopRecording()
		a.stopTake()
		a.playVocals(at)
	}
}
