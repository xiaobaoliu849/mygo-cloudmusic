package teacher

import (
	"os"
	"testing"
	"time"
)

// Gemini, set up as the app sets it up, greets the listener when asked,
// out loud. It runs with LIVE set and a key in GEMINI_API_KEY.
func TestGeminiLive(t *testing.T) {
	key := os.Getenv("GEMINI_API_KEY")
	if os.Getenv("LIVE") == "" || key == "" {
		t.Skip("set LIVE and GEMINI_API_KEY to talk with Gemini")
	}
	ch := make(chan Event, 1024)
	start := time.Now()
	s, err := DialGemini(Config{Key: key, Instructions: "你是声乐老师，用简体中文说话，每次一句。", Tools: []Tool{{Name: "play_line", Description: "放第 n 句", Params: map[string]any{
		"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}},
	}}}}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("set up in %v", time.Since(start))
	s.Tell("你好，用一句话打个招呼。")
	s.Respond()
	asked := time.Now()
	spoke := false
	for deadline := time.After(30 * time.Second); ; {
		select {
		case e := <-ch:
			switch e.Kind {
			case Answering:
				t.Logf("began in %v", time.Since(asked))
			case Speech:
				if !spoke {
					t.Logf("spoke in %v", time.Since(asked))
				}
				spoke = true
			case Saying:
				t.Logf("said %q", e.Text)
			case Failed, Closed:
				t.Fatalf("%v: %v", e.Kind, e.Err)
			case Answered:
				if !spoke {
					t.Fatal("answered without a word")
				}
				return
			}
		case <-deadline:
			t.Fatal("no answer in 30 s")
		}
	}
}
