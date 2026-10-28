package lyrics

import (
	"testing"
	"time"
)

func TestParseLRC(t *testing.T) {
	lines := mergeLyrics("[00:01.000] 作词 : 某人\n[00:30.54]故事的小黄花\n[00:34.165][01:10.1]从出生那年就飘着\n[00:40.00]\n",
		"[00:30.54]The little yellow flower\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines: %+v", len(lines), lines)
	}
	if lines[1].At != 30540*time.Millisecond || lines[1].Translation != "The little yellow flower" {
		t.Errorf("line 1: %+v", lines[1])
	}
	if lines[3].At != 70100*time.Millisecond || lines[3].Text != "从出生那年就飘着" {
		t.Errorf("line 3: %+v", lines[3])
	}
	if i := Index(lines, 33*time.Second); i != 1 {
		t.Errorf("index at 33s = %d", i)
	}
	if i := Index(lines, 0); i != -1 {
		t.Errorf("index at 0 = %d", i)
	}
}

func TestParseYRC(t *testing.T) {
	yrc := `{"t":0,"c":[{"tx":"作词: "},{"tx":"某人"}]}
[13010,4780](13010,660,0)从(13670,670,0)不(14340,30,0) (14370,480,0)主
[17790,1770](17790,170,0)I(17960,450,0) (18410,290,0)love`
	lines := Build("", "[00:13.00]Never\n[00:17.80]我爱", yrc)
	if len(lines) != 2 || !lines[0].Timed {
		t.Fatalf("lines %+v", lines)
	}
	l := lines[0]
	if l.Text != "从不 主" || l.At != 13010*time.Millisecond || l.End != 17790*time.Millisecond {
		t.Errorf("line %q %v-%v", l.Text, l.At, l.End)
	}
	if len(l.Words) != 3 || l.Words[1].Text != "不 " || l.Words[2].Start != 14370*time.Millisecond {
		t.Errorf("words %+v", l.Words)
	}
	if l.Translation != "Never" || lines[1].Translation != "我爱" {
		t.Errorf("translations %q %q", l.Translation, lines[1].Translation)
	}
}

// Romaji goes to the word-timed line nearest its time, as NetEase's
// romalrc follows the timed lines, not the word-timed ones; its credits
// and lines far from any sung line are left out.
func TestAddRomaji(t *testing.T) {
	yrc := `[57460,3800](57460,400,0)悴(57860,400,0)んだ(58260,400,0)心
[61340,4000](61340,400,0)世界(61740,400,0)で`
	lines := Build("", "", yrc)
	AddRomaji(lines, "[by:someone]\n[00:57.453]ka ji ka n da ko ko ro\n[01:01.336]se ka i de\n[01:30.00]tooi\n")
	if len(lines) != 2 || lines[0].Romaji != "ka ji ka n da ko ko ro" || lines[1].Romaji != "se ka i de" {
		t.Errorf("romaji %+v", lines)
	}
}

func TestSpreadWords(t *testing.T) {
	w := SpreadWords("我爱 you all", 0, 10*time.Second)
	got := []string{}
	for _, x := range w {
		got = append(got, x.Text)
	}
	if len(w) != 4 || got[0] != "我" || got[1] != "爱" || got[2] != "you " || got[3] != "all" {
		t.Fatalf("words %q", got)
	}
	if w[0].Start != 0 || w[3].End > 10*time.Second || w[1].Start != w[0].End {
		t.Errorf("times %+v", w)
	}
}

func TestQuiets(t *testing.T) {
	s := time.Second
	lines := []Line{
		{At: 0, End: 2 * s, Text: "作词 : 林夕"},
		{At: 2 * s, End: 4 * s, Text: "作曲：陈小霞"},
		{At: 20 * s, End: 25 * s, Text: "一"},
		{At: 26 * s, End: 30 * s, Text: "二"},
		{At: 45 * s, End: 50 * s, Text: "三"},
	}
	got := Quiets(lines, 60*s, 8*s)
	want := []Quiet{{0, 20 * s}, {30 * s, 45 * s}, {50 * s, 60 * s}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("quiet %d: %v, want %v", i, got[i], want[i])
		}
	}
	if q := Quiets(nil, 60*s, 8*s); len(q) != 0 {
		t.Errorf("a song without lyrics: %v", q)
	}
}

func TestIsCredit(t *testing.T) {
	for text, want := range map[string]bool{
		"作词 : 林夕":            true,
		"作曲：陈小霞":             true,
		"编曲: 藤田淳平(Elements)": true,
		"Producer: 周杰伦":      true,
		"我说：你好":              false,
		"时间：是一把刀":            false,
		"悴んだ心 ふるえる眼差し":       false,
		"词：方文山":              true,
		"曲终人散：空余叹":           false,
		"BOY(SoulJa):":       true,
		"GIRL(青山テルマ)":         true,
		"女：":                 true,
		"（BOY:君のぬくもり":        false,
		"Oh(oh)":             false,
		"女":                  false,
	} {
		if IsCredit(text) != want {
			t.Errorf("%q: %v", text, !want)
		}
	}
}

// A line goes on through the singer of the next named after it, to the
// next line sung: そばにいるね's "今じゃ殘された君はアルバムの中" stopped at
// "GIRL(青山テルマ)", short of its last words.
func TestLineEndsAtNextSung(t *testing.T) {
	lines := Build("[02:44.74]今じゃ殘された君はアルバムの中\n[02:47.20]GIRL(青山テルマ)\n[02:47.95]アルバムの中 納めた思い出の\n", "", "")
	if len(lines) != 3 {
		t.Fatalf("lines %+v", lines)
	}
	if want := 2*time.Minute + 47950*time.Millisecond; lines[0].End != want {
		t.Errorf("the line ends at %v, want %v", lines[0].End, want)
	}
	if lines[1].End != lines[2].At {
		t.Errorf("the singer's line ends at %v, want %v", lines[1].End, lines[2].At)
	}
}
