package teacher

import (
	"bufio"
	"cmp"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// GeminiURL is where sessions with Gemini connect, over Google's Live API,
// with an API key of Google AI Studio.
const GeminiURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// GeminiModel and GeminiVoice are the model a Gemini session talks with,
// and its voice, unless its Config says.
const (
	GeminiModel = "gemini-3.8-live"
	GeminiVoice = "Sulafat" // warm, as a teacher's
)

// Gemini is the Provider of a Config that talks with Gemini, not DashScope.
const Gemini = "gemini"

// A GeminiSession is a conversation with Gemini. It answers to what the
// app asks as a Session does, though Gemini goes about it otherwise: it
// goes on of itself once told what its calls came to, and cannot be
// stopped short, so its turns are told as they begin and end.
type GeminiSession struct {
	ws   *websocket.Conn
	mu   sync.Mutex // sends
	done chan struct{}
	once sync.Once

	st sync.Mutex
	// names are the tools called, by the id of their call: Gemini wants a
	// call's name with what it came to.
	names map[string]string
	// told is what the session was told, to send as the listener's turn
	// once a response is asked for.
	told []map[string]any
	// goesOn says Gemini was told what its calls came to, and goes on of
	// itself: a response asked for then is on its way.
	goesOn bool
	// awaited says a response was asked for, or comes of what calls came
	// to, and has not begun: one that ends with nothing said, as when the
	// teacher keeps quiet while the listener sings, is an answer all the
	// same.
	awaited atomic.Bool
}

// DialGemini connects to Gemini, sets the session up, and gives got what it
// hears from the model, on a goroutine of its own, until Close or the
// connection fails, Closed last.
func DialGemini(cfg Config, got func(Event)) (*GeminiSession, error) {
	if cfg.Key == "" {
		return nil, errors.New("teacher: no API key")
	}
	wc, err := websocket.NewConfig(cmp.Or(cfg.URL, GeminiURL)+"?key="+url.QueryEscape(cfg.Key), "http://localhost/")
	if err != nil {
		return nil, err
	}
	wc.Dialer = &net.Dialer{Timeout: 10 * time.Second}
	ws, err := dialThroughProxy(wc)
	if err != nil {
		return nil, fmt.Errorf("teacher: connecting to Gemini: %w", err)
	}
	s := &GeminiSession{ws: ws, done: make(chan struct{}), names: map[string]string{}}
	if err := s.send(geminiSetup(cfg)); err != nil {
		ws.Close()
		return nil, err
	}
	// Nothing may be sent before the session is set up.
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var m geminiMessage
	if err := s.receive(&m); err != nil {
		ws.Close()
		return nil, fmt.Errorf("teacher: Gemini refused the session (is the key or model right?): %w", err)
	}
	if m.SetupComplete == nil {
		ws.Close()
		return nil, errors.New("teacher: Gemini did not set the session up")
	}
	ws.SetReadDeadline(time.Time{})
	go s.read(got)
	return s, nil
}

// dialThroughProxy connects as wc says, through the proxy the environment
// names for it (HTTPS_PROXY), as Google is reached from where it is not
// otherwise; directly without one.
func dialThroughProxy(wc *websocket.Config) (*websocket.Conn, error) {
	target := *wc.Location
	target.Scheme = strings.Replace(target.Scheme, "ws", "http", 1)
	proxy, err := http.ProxyFromEnvironment(&http.Request{URL: &target})
	if err != nil || proxy == nil {
		return websocket.DialConfig(wc)
	}
	host := wc.Location.Host
	if wc.Location.Port() == "" {
		host = net.JoinHostPort(wc.Location.Hostname(), map[string]string{"wss": "443", "ws": "80"}[wc.Location.Scheme])
	}
	conn, err := wc.Dialer.Dial("tcp", proxy.Host)
	if err != nil {
		return nil, fmt.Errorf("the proxy %s: %w", proxy.Host, err)
	}
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err == nil && res.StatusCode != http.StatusOK {
		err = fmt.Errorf("the proxy %s said %s", proxy.Host, res.Status)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	if wc.Location.Scheme == "wss" {
		tc := tls.Client(conn, &tls.Config{ServerName: wc.Location.Hostname()})
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	ws, err := websocket.NewClient(wc, conn)
	if err != nil {
		conn.Close()
	}
	return ws, err
}

// geminiSetup sets the session up: the voice, the instructions and the
// tools; what is heard and said is written down, and the model answers
// once the listener stops for a moment.
func geminiSetup(cfg Config) map[string]any {
	decls := make([]map[string]any, len(cfg.Tools))
	for i, t := range cfg.Tools {
		params := t.Params
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		decls[i] = map[string]any{"name": t.Name, "description": t.Description, "parametersJsonSchema": params}
	}
	return map[string]any{"setup": map[string]any{
		"model": "models/" + cmp.Or(cfg.Model, GeminiModel),
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{"voiceConfig": map[string]any{
				"prebuiltVoiceConfig": map[string]any{"voiceName": cmp.Or(cfg.Voice, GeminiVoice)},
			}},
		},
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": cfg.Instructions}}},
		"tools":             []map[string]any{{"functionDeclarations": decls}},
		// As DashScope's: a singer breathes between phrases.
		"realtimeInputConfig": map[string]any{"automaticActivityDetection": map[string]any{
			"prefixPaddingMs": 300, "silenceDurationMs": 800,
		}},
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
		// A lesson goes on past the length a session may otherwise take.
		"contextWindowCompression": map[string]any{"slidingWindow": map[string]any{}},
	}}
}

// Hear gives the model more of what the microphone hears, mono at InRate.
func (s *GeminiSession) Hear(samples []float32) error {
	pcm := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(math.Round(float64(min(max(v, -1), 1))*32767))))
	}
	return s.send(map[string]any{"realtimeInput": map[string]any{"audio": map[string]any{
		"mimeType": fmt.Sprintf("audio/pcm;rate=%d", InRate), "data": base64.StdEncoding.EncodeToString(pcm),
	}}})
}

// Instruct tells the model its instructions changed, as for another song:
// Gemini keeps those it was set up with, so they come as a note it does
// not answer.
func (s *GeminiSession) Instruct(instructions string) error {
	return s.send(map[string]any{"clientContent": map[string]any{
		"turns":        []map[string]any{userTurn("（上课说明更新了，以这份为准，不用回答）\n" + instructions)},
		"turnComplete": false,
	}})
}

// Tell gives the model a message, as from the listener, in text, sent once
// a response is asked for.
func (s *GeminiSession) Tell(text string) error {
	s.st.Lock()
	defer s.st.Unlock()
	s.told = append(s.told, userTurn(text))
	return nil
}

// Answer gives the model what its call id came to; it goes on of itself.
func (s *GeminiSession) Answer(id string, result any) error {
	s.st.Lock()
	name := s.names[id]
	s.goesOn = true
	s.st.Unlock()
	s.awaited.Store(true)
	return s.send(map[string]any{"toolResponse": map[string]any{"functionResponses": []map[string]any{{
		"id": id, "name": name, "response": map[string]any{"output": result},
	}}}})
}

// Respond asks the model to go on: with what it was told, or, told what
// its calls came to, it goes on already; else it is asked to answer the
// listener.
func (s *GeminiSession) Respond() error {
	s.st.Lock()
	turns := s.told
	s.told = nil
	goesOn := s.goesOn
	s.goesOn = false
	s.st.Unlock()
	if len(turns) == 0 {
		if goesOn {
			return nil
		}
		turns = []map[string]any{userTurn("（系统提醒，不是学生说的：请接着上课，回应学生刚才说的。）")}
	}
	s.awaited.Store(true)
	return s.send(map[string]any{"clientContent": map[string]any{"turns": turns, "turnComplete": true}})
}

// Cancel does nothing: Gemini's turn cannot be stopped short. What is left
// of it ends as it would, and the app drops it.
func (s *GeminiSession) Cancel() error { return nil }

// Close ends the session.
func (s *GeminiSession) Close() {
	s.once.Do(func() {
		close(s.done)
		s.ws.Close()
	})
}

func userTurn(text string) map[string]any {
	return map[string]any{"role": "user", "parts": []map[string]any{{"text": text}}}
}

func (s *GeminiSession) send(m map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return websocket.JSON.Send(s.ws, m)
}

// receive reads a message, which Gemini sends as text or binary.
func (s *GeminiSession) receive(m *geminiMessage) error {
	var raw []byte
	if err := websocket.Message.Receive(s.ws, &raw); err != nil {
		return err
	}
	*m = geminiMessage{}
	return json.Unmarshal(raw, m)
}

// geminiMessage is what Gemini sends, of what the app hears.
type geminiMessage struct {
	SetupComplete *struct{} `json:"setupComplete"`
	ServerContent *struct {
		ModelTurn *struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		InputTranscription  *struct{ Text string } `json:"inputTranscription"`
		OutputTranscription *struct{ Text string } `json:"outputTranscription"`
		TurnComplete        bool                   `json:"turnComplete"`
		Interrupted         bool                   `json:"interrupted"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []struct {
			ID   string         `json:"id"`
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"functionCalls"`
	} `json:"toolCall"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// read hears the model, giving got what it says, until the end. Gemini
// says nothing of a turn beginning, nor ends one in calls: its turns are
// told to begin as it first says or calls something, and end as it turns
// to the listener, or calls, waiting for what the calls come to. What the
// listener said comes bit by bit, and is told as heard as the model
// answers it.
func (s *GeminiSession) read(got func(Event)) {
	got(Event{Kind: Ready})
	var heard strings.Builder
	inTurn := false
	flushHeard := func() {
		if text := strings.TrimSpace(heard.String()); text != "" {
			got(Event{Kind: Heard, Text: text})
		}
		heard.Reset()
	}
	begin := func() {
		if !inTurn {
			flushHeard()
			inTurn = true
			s.awaited.Store(false)
			got(Event{Kind: Answering})
		}
	}
	end := func() {
		if inTurn {
			flushHeard()
			inTurn = false
			got(Event{Kind: Answered})
		}
	}
	for {
		var m geminiMessage
		if err := s.receive(&m); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				continue
			}
			select {
			case <-s.done:
				err = nil
			default:
				err = fmt.Errorf("teacher: connection to Gemini lost: %w", err)
			}
			got(Event{Kind: Closed, Err: err})
			return
		}
		if e := m.Error; e != nil {
			got(Event{Kind: Failed, Err: fmt.Errorf("teacher: %d: %s", e.Code, e.Message)})
		}
		if sc := m.ServerContent; sc != nil {
			if t := sc.InputTranscription; t != nil && t.Text != "" {
				heard.WriteString(t.Text)
				got(Event{Kind: Hearing, Text: heard.String()})
			}
			if t := sc.OutputTranscription; t != nil && t.Text != "" {
				begin()
				got(Event{Kind: Saying, Text: t.Text})
			}
			if mt := sc.ModelTurn; mt != nil {
				for _, p := range mt.Parts {
					if p.InlineData == nil || !strings.HasPrefix(p.InlineData.MimeType, "audio/pcm") {
						continue
					}
					if audio := decodePCM(p.InlineData.Data); len(audio) > 0 {
						begin()
						got(Event{Kind: Speech, Audio: audio})
					}
				}
			}
			if (sc.TurnComplete || sc.Interrupted) && !inTurn && s.awaited.Load() {
				// The answer asked for, with nothing said.
				begin()
			}
			if sc.TurnComplete || sc.Interrupted {
				end()
			}
		}
		if tc := m.ToolCall; tc != nil && len(tc.FunctionCalls) > 0 {
			begin()
			s.st.Lock()
			for _, c := range tc.FunctionCalls {
				s.names[c.ID] = c.Name
			}
			s.st.Unlock()
			for _, c := range tc.FunctionCalls {
				args := c.Args
				if args == nil {
					args = map[string]any{}
				}
				got(Event{Kind: Calling, Call: Call{ID: c.ID, Name: c.Name, Args: args}})
			}
			end()
		}
	}
}

// decodePCM makes samples of base64 16-bit PCM.
func decodePCM(data string) []float32 {
	pcm, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(pcm) < 2 {
		return nil
	}
	audio := make([]float32, len(pcm)/2)
	for i := range audio {
		audio[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	return audio
}
