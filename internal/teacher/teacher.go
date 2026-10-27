// Package teacher talks with a voice model in real time, over the Realtime
// API of Alibaba Cloud's DashScope: the model hears the listener through
// the microphone, speaks back, and calls the app's tools to run a lesson.
package teacher

import (
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// The rates of what the model hears and says, mono 16-bit PCM.
const (
	InRate  = 16000
	OutRate = 24000
)

// URL is where sessions connect, the model named after it.
const URL = "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"

// DefaultModel and DefaultVoice are the model a session talks with, and
// its voice, unless its Config says.
const (
	DefaultModel = "qwen3.8-omni-flash-realtime"
	DefaultVoice = "Tina"
)

// A Config is how a session goes.
type Config struct {
	URL          string // URL if empty
	Key          string // the DashScope API key
	Model, Voice string // DefaultModel and DefaultVoice if empty
	Instructions string
	Tools        []Tool
}

// A Tool is one the model may call: its parameters a JSON schema.
type Tool struct {
	Name, Description string
	Params            map[string]any
}

// A Call is the model calling a tool.
type Call struct {
	ID, Name string
	Args     map[string]any
}

// An Event is something the session hears from the model.
type Event struct {
	Kind  Kind
	Text  string    // Hearing, Heard, Saying
	Audio []float32 // Speech: mono at OutRate
	Call  Call      // Calling
	Err   error     // Failed, Closed
}

// Kind is what an Event is.
type Kind int

const (
	Ready     Kind = iota // the session is set up
	Hearing               // what the listener says so far, Text, to be revised
	Heard                 // what the listener said, Text
	Saying                // more of what the model says, Text
	Speech                // more of the model's voice, Audio
	Answering             // the model starts to respond
	Calling               // the model calls a tool, Call
	Answered              // the model's response is done
	Failed                // the model reports an error, Err; the session goes on
	Closed                // the session ended, with Err unless asked to
)

// A Session is a conversation with the model.
type Session struct {
	ws   *websocket.Conn
	mu   sync.Mutex // sends
	done chan struct{}
	once sync.Once
}

// Dial connects to the model, sets the session up, and gives got what it
// hears from the model, on a goroutine of its own, until Close or the
// connection fails, Closed last.
func Dial(cfg Config, got func(Event)) (*Session, error) {
	if cfg.Key == "" {
		return nil, errors.New("teacher: no API key")
	}
	url := cmp.Or(cfg.URL, URL) + "?model=" + cmp.Or(cfg.Model, DefaultModel)
	wc, err := websocket.NewConfig(url, "http://localhost/")
	if err != nil {
		return nil, err
	}
	wc.Header.Set("Authorization", "Bearer "+cfg.Key)
	wc.Dialer = &net.Dialer{Timeout: 10 * time.Second}
	ws, err := websocket.DialConfig(wc)
	if err != nil {
		return nil, fmt.Errorf("teacher: connecting: %w", err)
	}
	s := &Session{ws: ws, done: make(chan struct{})}
	if err := s.send(sessionUpdate(cfg)); err != nil {
		ws.Close()
		return nil, err
	}
	go s.read(got)
	return s, nil
}

// sessionUpdate sets the session up: the voice, the instructions and the
// tools; what is heard is written down, and the model answers once the
// listener stops for a moment. Only the first session.update sets all
// of this: a later one leaves the model deaf.
func sessionUpdate(cfg Config) map[string]any {
	tools := make([]map[string]any, len(cfg.Tools))
	for i, t := range cfg.Tools {
		params := t.Params
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools[i] = map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": params,
		}}
	}
	return map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"modalities":                []string{"text", "audio"},
			"voice":                     cmp.Or(cfg.Voice, DefaultVoice),
			"instructions":              cfg.Instructions,
			"input_audio_format":        "pcm16",
			"output_audio_format":       "pcm16",
			"input_audio_transcription": map[string]any{"model": "qwen3-asr-flash-realtime"},
			// A singer breathes between phrases: longer than a speaker's
			// pause before the model takes its turn.
			"turn_detection": map[string]any{
				"type": "server_vad", "threshold": 0.2, "prefix_padding_ms": 300,
				"silence_duration_ms": 1200, "interrupt_response": false,
			},
			"tools": tools,
		},
	}
}

// Instruct changes the model's instructions, as for another song.
func (s *Session) Instruct(instructions string) error {
	return s.send(map[string]any{"type": "session.update", "session": map[string]any{"instructions": instructions}})
}

// Hear gives the model more of what the microphone hears, mono at InRate.
func (s *Session) Hear(samples []float32) error {
	pcm := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(math.Round(float64(min(max(v, -1), 1))*32767))))
	}
	return s.send(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)})
}

// Answer gives the model what its call id came to, as JSON.
func (s *Session) Answer(id string, result any) error {
	out, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{"type": "function_call_output", "call_id": id, "output": string(out)},
	})
}

// Tell gives the model a message, as from the listener, in text: to
// start the conversation, as the model answers nothing before it hears
// something.
func (s *Session) Tell(text string) error {
	return s.send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		},
	})
}

// Respond asks the model to go on, as with what its calls came to.
func (s *Session) Respond() error { return s.send(map[string]any{"type": "response.create"}) }

// Close ends the session.
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.done)
		s.send(map[string]any{"type": "session.finish"})
		s.ws.Close()
	})
}

func (s *Session) send(event map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return websocket.JSON.Send(s.ws, event)
}

// read hears the model, giving got what it says, until the end.
func (s *Session) read(got func(Event)) {
	for {
		var raw json.RawMessage
		if err := websocket.JSON.Receive(s.ws, &raw); err != nil {
			select {
			case <-s.done:
				err = nil
			default:
				err = fmt.Errorf("teacher: connection lost: %w", err)
			}
			got(Event{Kind: Closed, Err: err})
			return
		}
		if e, ok := parse(raw); ok {
			got(e)
		}
	}
}

// parse makes an Event of what the model sends, reporting false for what
// the app need not hear.
func parse(raw json.RawMessage) (Event, bool) {
	var m struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Text       string `json:"text"`
		Stash      string `json:"stash"`
		Transcript string `json:"transcript"`
		CallID     string `json:"call_id"`
		Name       string `json:"name"`
		Arguments  string `json:"arguments"`
		Error      *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		// An error may come with no type, and these at the top.
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return Event{}, false
	}
	switch m.Type {
	case "session.updated":
		return Event{Kind: Ready}, true
	case "conversation.item.input_audio_transcription.delta", "conversation.item.input_audio_transcription.text":
		return Event{Kind: Hearing, Text: m.Text + m.Stash + m.Delta}, true
	case "conversation.item.input_audio_transcription.completed":
		return Event{Kind: Heard, Text: strings.TrimSpace(m.Transcript)}, true
	case "response.audio_transcript.delta", "response.text.delta":
		return Event{Kind: Saying, Text: m.Delta}, m.Delta != ""
	case "response.audio.delta":
		pcm, err := base64.StdEncoding.DecodeString(m.Delta)
		if err != nil || len(pcm) < 2 {
			return Event{}, false
		}
		audio := make([]float32, len(pcm)/2)
		for i := range audio {
			audio[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		}
		return Event{Kind: Speech, Audio: audio}, true
	case "response.function_call_arguments.done":
		args := map[string]any{}
		json.Unmarshal([]byte(m.Arguments), &args)
		return Event{Kind: Calling, Call: Call{ID: m.CallID, Name: m.Name, Args: args}}, true
	case "response.created":
		return Event{Kind: Answering}, true
	case "response.done":
		return Event{Kind: Answered}, true
	case "error", "":
		code, msg := m.Code, m.Message
		if m.Error != nil {
			code, msg = m.Error.Code, m.Error.Message
		}
		if code == "" && msg == "" {
			return Event{}, false
		}
		if code == "" {
			return Event{Kind: Failed, Err: fmt.Errorf("teacher: %s", msg)}, true
		}
		return Event{Kind: Failed, Err: fmt.Errorf("teacher: %s: %s", code, msg)}, true
	}
	return Event{}, false
}
