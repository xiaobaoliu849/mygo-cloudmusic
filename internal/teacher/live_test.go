package teacher

import (
	"os"
	"testing"
	"time"
)

// With LIVE and DASHSCOPE_API_KEY set, a session with the real model is
// set up, tools and all, and answers a message with speech.
func TestLiveSession(t *testing.T) {
	key := os.Getenv("DASHSCOPE_API_KEY")
	if os.Getenv("LIVE") == "" || key == "" {
		t.Skip("set LIVE and DASHSCOPE_API_KEY to talk with the model")
	}
	ch := make(chan Event, 256)
	s, err := Dial(Config{Key: key, Instructions: "你是声乐老师。用一句中文打招呼。", Tools: []Tool{{Name: "play_line", Description: "plays a line"}}}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait := func(k Kind) Event {
		t.Helper()
		timeout := time.After(20 * time.Second)
		for {
			select {
			case e := <-ch:
				if e.Kind == Failed || e.Kind == Closed {
					t.Fatalf("waiting for %d: %v", k, e.Err)
				}
				if e.Kind == k {
					return e
				}
			case <-timeout:
				t.Fatalf("no event %d", k)
			}
		}
	}
	wait(Ready)
	s.Tell("你好，我准备好了")
	s.Respond()
	var said []float32
	for {
		e := wait(Speech)
		said = append(said, e.Audio...)
		if len(said) > OutRate/2 {
			break
		}
	}
	t.Logf("the model spoke %v", time.Duration(len(said))*time.Second/OutRate)
}

// With LIVE and DASHSCOPE_API_KEY set, the real model, asked to play a
// line, calls the tool for it, with the line.
func TestLiveToolCall(t *testing.T) {
	key := os.Getenv("DASHSCOPE_API_KEY")
	if os.Getenv("LIVE") == "" || key == "" {
		t.Skip("set LIVE and DASHSCOPE_API_KEY to talk with the model")
	}
	ch := make(chan Event, 1024)
	tool := Tool{Name: "play_line", Description: "把第 n 句放给学生听", Params: map[string]any{
		"type":       "object",
		"properties": map[string]any{"n": map[string]any{"type": "integer", "description": "行号，从 1 开始"}},
		"required":   []string{"n"},
	}}
	s, err := Dial(Config{Key: key, Instructions: "你是声乐老师，用工具放歌给学生听。", Tools: []Tool{tool}}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	timeout := time.After(30 * time.Second)
	for ready := false; ; {
		select {
		case e := <-ch:
			switch e.Kind {
			case Ready:
				if !ready {
					ready = true
					s.Tell("请放第一句给我听")
					s.Respond()
				}
			case Calling:
				if e.Call.Name != "play_line" || e.Call.Args["n"] != 1.0 {
					t.Fatalf("called %+v", e.Call)
				}
				return
			case Failed, Closed:
				t.Fatal(e.Err)
			case Answered:
				t.Fatal("answered without calling the tool")
			}
		case <-timeout:
			t.Fatal("no call")
		}
	}
}
