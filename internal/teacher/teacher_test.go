package teacher

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// fakeModel stands in for DashScope: it records what the session sends,
// and sends what the test gives it.
type fakeModel struct {
	auth, model string
	got         chan map[string]any
	send        chan any
}

func newFakeModel(t *testing.T) (*fakeModel, string) {
	f := &fakeModel{got: make(chan map[string]any, 16), send: make(chan any, 16)}
	ws := websocket.Server{Handler: func(c *websocket.Conn) {
		go func() {
			for e := range f.send {
				websocket.JSON.Send(c, e)
			}
		}()
		for {
			var m map[string]any
			if websocket.JSON.Receive(c, &m) != nil {
				return
			}
			f.got <- m
		}
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth, f.model = r.Header.Get("Authorization"), r.URL.Query().Get("model")
		ws.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func (f *fakeModel) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m := <-f.got:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("the session sent nothing")
		return nil
	}
}

func events(t *testing.T, ch chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

// A session connects with the key and model, sets itself up with the
// tools as DashScope declares them, sends what the microphone hears as
// 16-bit PCM, and makes events of what the model says and calls.
func TestSession(t *testing.T) {
	f, url := newFakeModel(t)
	ch := make(chan Event, 16)
	s, err := Dial(Config{URL: url, Key: "test-key", Instructions: "教唱歌", Tools: []Tool{{Name: "play_line", Description: "plays a line"}}}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	up := f.next(t)
	if f.auth != "Bearer test-key" || f.model != DefaultModel {
		t.Errorf("connected with %q to %q", f.auth, f.model)
	}
	session := up["session"].(map[string]any)
	if up["type"] != "session.update" || session["instructions"] != "教唱歌" || session["input_audio_format"] != "pcm16" {
		t.Errorf("set up with %v", up)
	}
	tool := session["tools"].([]any)[0].(map[string]any)
	if fn := tool["function"].(map[string]any); tool["type"] != "function" || fn["name"] != "play_line" || fn["parameters"] == nil {
		t.Errorf("declared %v", tool)
	}

	s.Hear([]float32{0.5, -1, 0})
	m := f.next(t)
	pcm, _ := base64.StdEncoding.DecodeString(m["audio"].(string))
	if m["type"] != "input_audio_buffer.append" || len(pcm) != 6 || int16(binary.LittleEndian.Uint16(pcm[2:])) != -32767 {
		t.Errorf("sent %v", m)
	}

	voice := make([]byte, 4)
	binary.LittleEndian.PutUint16(voice[2:], uint16(0x4000))
	f.send <- map[string]any{"type": "session.updated"}
	f.send <- map[string]any{"type": "conversation.item.input_audio_transcription.completed", "transcript": " 春日影 "}
	f.send <- map[string]any{"type": "response.created"}
	f.send <- map[string]any{"type": "response.audio_transcript.delta", "delta": "很好"}
	f.send <- map[string]any{"type": "response.audio.delta", "delta": base64.StdEncoding.EncodeToString(voice)}
	f.send <- map[string]any{"type": "response.function_call_arguments.done", "call_id": "c1", "name": "play_line", "arguments": `{"n":3}`}
	f.send <- map[string]any{"type": "response.done"}
	f.send <- map[string]any{"code": "AccessDenied", "message": "Access denied"}
	if e := events(t, ch); e.Kind != Ready {
		t.Errorf("first %v", e)
	}
	if e := events(t, ch); e.Kind != Heard || e.Text != "春日影" {
		t.Errorf("heard %+v", e)
	}
	if e := events(t, ch); e.Kind != Answering {
		t.Errorf("answering %+v", e)
	}
	if e := events(t, ch); e.Kind != Saying || e.Text != "很好" {
		t.Errorf("saying %+v", e)
	}
	if e := events(t, ch); e.Kind != Speech || len(e.Audio) != 2 || e.Audio[1] != 0.5 {
		t.Errorf("speech %+v", e)
	}
	if e := events(t, ch); e.Kind != Calling || e.Call.ID != "c1" || e.Call.Name != "play_line" || e.Call.Args["n"] != 3.0 {
		t.Errorf("call %+v", e)
	}
	if e := events(t, ch); e.Kind != Answered {
		t.Errorf("done %+v", e)
	}
	if e := events(t, ch); e.Kind != Failed || !strings.Contains(e.Err.Error(), "AccessDenied") {
		t.Errorf("an untyped error came as %+v", e)
	}

	s.Answer("c1", map[string]any{"line": 3})
	m = f.next(t)
	item := m["item"].(map[string]any)
	var out map[string]any
	json.Unmarshal([]byte(item["output"].(string)), &out)
	if m["type"] != "conversation.item.create" || item["type"] != "function_call_output" || item["call_id"] != "c1" || out["line"] != 3.0 {
		t.Errorf("answered %v", m)
	}
	s.Respond()
	if m := f.next(t); m["type"] != "response.create" {
		t.Errorf("responded %v", m)
	}
	s.Tell("你好")
	m = f.next(t)
	item = m["item"].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" || content["type"] != "input_text" || content["text"] != "你好" {
		t.Errorf("told %v", m)
	}

	s.Close()
	if m := f.next(t); m["type"] != "session.finish" {
		t.Errorf("finished with %v", m)
	}
	if e := events(t, ch); e.Kind != Closed || e.Err != nil {
		t.Errorf("closed %+v", e)
	}
}

// With no key, a session does not try to connect.
func TestDialNoKey(t *testing.T) {
	if _, err := Dial(Config{URL: "ws://127.0.0.1:1"}, func(Event) {}); err == nil {
		t.Error("dialed with no key")
	}
}

// A voice plays what is said as it comes, waiting for it rather than
// running ahead in silence, and says it is speaking until it has been
// heard; closed, it ends.
func TestVoice(t *testing.T) {
	v := NewVoice()
	read := make(chan int)
	go func() {
		n, _ := v.Read(make([]float32, 100))
		read <- n
	}()
	select {
	case <-read:
		t.Fatal("read before anything was said")
	case <-time.After(50 * time.Millisecond):
	}
	if v.Speaking(0) {
		t.Error("speaking before saying anything")
	}
	v.Say(make([]float32, OutRate/2))
	if n := <-read; n != 100 {
		t.Errorf("read %d", n)
	}
	v.Read(make([]float32, OutRate))
	if !v.Speaking(100 * time.Millisecond) {
		t.Error("not speaking, half a second said and 100 ms heard")
	}
	if v.Speaking(500 * time.Millisecond) {
		t.Error("speaking once all of it was heard")
	}
	v.Close()
	if _, err := v.Read(make([]float32, 10)); err != io.EOF {
		t.Errorf("closed, read %v", err)
	}
}
