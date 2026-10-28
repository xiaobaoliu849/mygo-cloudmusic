package teacher

import (
	"encoding/base64"
	"encoding/binary"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// A Gemini session connects with the key, sets itself up with the model,
// instructions and tools as Gemini declares them, and waits until it is
// set up; it sends what the microphone hears, and what it is told once a
// response is asked for. Gemini's turns, which it does not say begin, nor
// end in calls, come as the app's: what the listener said heard as the
// model answers, a turn begun as it first speaks or calls, and ended as it
// calls. Told what a call came to, Gemini goes on of itself, and is not
// asked again.
func TestGeminiSession(t *testing.T) {
	f, url := newFakeModel(t)
	f.send <- map[string]any{"setupComplete": map[string]any{}}
	ch := make(chan Event, 32)
	s, err := DialGemini(Config{URL: url, Key: "g-key", Instructions: "教唱歌", Tools: []Tool{{Name: "play_line", Description: "plays a line"}}}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	setup := f.next(t)["setup"].(map[string]any)
	if f.key != "g-key" || setup["model"] != "models/"+GeminiModel {
		t.Errorf("connected with %q to %v", f.key, setup["model"])
	}
	if text := setup["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]; text != "教唱歌" {
		t.Errorf("instructed %v", text)
	}
	decl := setup["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	if decl["name"] != "play_line" || decl["parametersJsonSchema"] == nil {
		t.Errorf("declared %v", decl)
	}
	// Gemini 3.8 Live does not wait on a call unless told: it talked on
	// over the line it played, and the connection ended.
	if decl["behavior"] != "BLOCKING" {
		t.Errorf("declared to run %v", decl["behavior"])
	}
	if e := events(t, ch); e.Kind != Ready {
		t.Errorf("first %+v", e)
	}

	s.Hear([]float32{0.5, -1, 0})
	in := f.next(t)["realtimeInput"].(map[string]any)["audio"].(map[string]any)
	pcm, _ := base64.StdEncoding.DecodeString(in["data"].(string))
	if in["mimeType"] != "audio/pcm;rate=16000" || len(pcm) != 6 || int16(binary.LittleEndian.Uint16(pcm[2:])) != -32767 {
		t.Errorf("sent %v", in)
	}
	s.Tell("你好")
	s.Respond()
	cc := f.next(t)["clientContent"].(map[string]any)
	if text := cc["turns"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]; text != "你好" || cc["turnComplete"] != true {
		t.Errorf("told %v", cc)
	}

	voice := make([]byte, 4)
	binary.LittleEndian.PutUint16(voice[2:], uint16(0x4000))
	f.send <- map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "春日"}}}
	f.send <- map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "影"}}}
	f.send <- map[string]any{"serverContent": map[string]any{"outputTranscription": map[string]any{"text": "很好"}}}
	f.send <- map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{
		map[string]any{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(voice)}},
	}}}}
	f.send <- map[string]any{"serverContent": map[string]any{"turnComplete": true}}
	f.send <- map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "c1", "name": "play_line", "args": map[string]any{"n": 3}}}}}
	want := []func(Event) bool{
		func(e Event) bool { return e.Kind == Hearing && e.Text == "春日" },
		func(e Event) bool { return e.Kind == Hearing && e.Text == "春日影" },
		func(e Event) bool { return e.Kind == Heard && e.Text == "春日影" },
		func(e Event) bool { return e.Kind == Answering },
		func(e Event) bool { return e.Kind == Saying && e.Text == "很好" },
		func(e Event) bool { return e.Kind == Speech && len(e.Audio) == 2 && e.Audio[1] == 0.5 },
		func(e Event) bool { return e.Kind == Answered },
		func(e Event) bool { return e.Kind == Answering },
		func(e Event) bool {
			return e.Kind == Calling && e.Call.ID == "c1" && e.Call.Name == "play_line" && e.Call.Args["n"] == 3.0
		},
		func(e Event) bool { return e.Kind == Answered },
	}
	for i, ok := range want {
		if e := events(t, ch); !ok(e) {
			t.Errorf("event %d: %+v", i, e)
		}
	}

	s.Answer("c1", map[string]any{"line": 3})
	fr := f.next(t)["toolResponse"].(map[string]any)["functionResponses"].([]any)[0].(map[string]any)
	if fr["id"] != "c1" || fr["name"] != "play_line" || fr["response"].(map[string]any)["output"].(map[string]any)["line"] != 3.0 {
		t.Errorf("answered %v", fr)
	}
	// It goes on of itself: asked to, nothing is sent; asked again, as
	// when it did not, it is reminded.
	s.Respond()
	s.Respond()
	cc = f.next(t)["clientContent"].(map[string]any)
	if text := cc["turns"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "系统提醒") {
		t.Errorf("asked again with %v", cc)
	}

	// Answered with nothing said, as the teacher keeps quiet while the
	// listener sings, it is an answer all the same: the app waited on it,
	// then asked again, and the teacher talked over the singing.
	f.send <- map[string]any{"serverContent": map[string]any{"turnComplete": true}}
	if e := events(t, ch); e.Kind != Answering {
		t.Errorf("an empty answer began as %+v", e)
	}
	if e := events(t, ch); e.Kind != Answered {
		t.Errorf("an empty answer ended as %+v", e)
	}
	// Not asked for, a turn ending again is nothing.
	f.send <- map[string]any{"serverContent": map[string]any{"turnComplete": true}}
	f.send <- map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "嗯"}}}
	if e := events(t, ch); e.Kind != Hearing {
		t.Errorf("a turn not asked for ended as %+v", e)
	}

	s.Close()
	for e := events(t, ch); e.Kind != Closed; e = events(t, ch) {
	}
}

// Gemini ends a connection after some minutes: the session resumes over
// another with the handle it was last given, and goes on. It ended the
// lesson instead, "connection to Gemini lost: EOF".
func TestGeminiResumes(t *testing.T) {
	setups := make(chan map[string]any, 4)
	got := make(chan map[string]any, 16)
	var conns atomic.Int32
	ws := websocket.Server{Handler: func(c *websocket.Conn) {
		var setup map[string]any
		if websocket.JSON.Receive(c, &setup) != nil {
			return
		}
		setups <- setup["setup"].(map[string]any)
		websocket.JSON.Send(c, map[string]any{"setupComplete": map[string]any{}})
		if conns.Add(1) == 1 {
			websocket.JSON.Send(c, map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": "h1", "resumable": true}})
			// Not resumable at this point: not the handle to resume with.
			websocket.JSON.Send(c, map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": "h2", "resumable": false}})
			time.Sleep(50 * time.Millisecond)
			return
		}
		for {
			var m map[string]any
			if websocket.JSON.Receive(c, &m) != nil {
				return
			}
			got <- m
		}
	}}
	srv := httptest.NewServer(ws)
	defer srv.Close()

	ch := make(chan Event, 16)
	s, err := DialGemini(Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Key: "g-key"}, func(e Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := <-setups
	if r, ok := first["sessionResumption"].(map[string]any); !ok || r["handle"] != nil {
		t.Errorf("first set up to resume %v", first["sessionResumption"])
	}
	if e := events(t, ch); e.Kind != Ready {
		t.Errorf("first %+v", e)
	}
	if e := events(t, ch); e.Kind != Resumed {
		t.Fatalf("as the connection ended: %+v", e)
	}
	select {
	case again := <-setups:
		if h := again["sessionResumption"].(map[string]any)["handle"]; h != "h1" {
			t.Errorf("resumed with %v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not connect again")
	}
	s.Tell("你好")
	s.Respond()
	select {
	case m := <-got:
		if m["clientContent"] == nil {
			t.Errorf("sent %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sent nothing over the new connection")
	}
}

// The reason Gemini ends a connection with is found in the last frame
// read: the websocket package read it as io.EOF, and the listener saw only
// "connection to Gemini lost: EOF".
func TestCloseReason(t *testing.T) {
	reason := "Request contains an invalid argument."
	frame := append([]byte{0x88, byte(2 + len(reason)), 0x03, 0xef}, reason...)
	c := &tailConn{tail: append([]byte(`{"serverContent":{}}`), frame...)}
	if got := c.closeReason(); got != "1007 "+reason {
		t.Errorf("closed with %q", got)
	}
	c.tail = append(c.tail, `{"more":1}`...)
	if got := c.closeReason(); got != "" {
		t.Errorf("not closed, read as closed with %q", got)
	}
}

// With no key, a Gemini session does not try to connect.
func TestDialGeminiNoKey(t *testing.T) {
	if _, err := DialGemini(Config{URL: "ws://127.0.0.1:1"}, func(Event) {}); err == nil {
		t.Error("dialed with no key")
	}
}
