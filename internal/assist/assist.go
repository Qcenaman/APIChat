package assist

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
	"github.com/qcenaman/apichat/internal/ts"
)

// Event goes to the UI.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

type Entry struct {
	Time   time.Time `json:"time"`
	Tool   string    `json:"tool"`
	Action string    `json:"action"`
	Args   any       `json:"args"`
	Result string    `json:"result"`
	Error  bool      `json:"error"`
	Millis int64     `json:"ms"`
}

type Session struct {
	ID      string    `json:"id"`
	Helper  string    `json:"helper"`
	Started time.Time `json:"started"`
	Until   time.Time `json:"until"`
	Count   int       `json:"count"`
}

type pending struct {
	id     string
	helper string
	mins   int
	ch     chan bool
}

type Manager struct {
	Guard *guard.Guard

	mu      sync.Mutex
	subs    map[chan Event]bool
	pend    *pending
	session *Session
	turn    *guard.Turn
	log     []Entry
	srv     *http.Server
	addr    string
}

func New(g *guard.Guard) *Manager {
	return &Manager{Guard: g, subs: map[chan Event]bool{}}
}

func (m *Manager) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 64)
	m.mu.Lock()
	m.subs[ch] = true
	if m.pend != nil {
		ch <- Event{Type: "assist_request", Data: map[string]any{"id": m.pend.id, "helper": m.pend.helper, "minutes": m.pend.mins}}
	}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Status is what the UI shows.
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]any{"listening": m.addr, "active": false}
	if m.session != nil && time.Now().Before(m.session.Until) {
		out["active"] = true
		out["session"] = m.session
	}
	if m.pend != nil {
		out["request"] = map[string]any{"id": m.pend.id, "helper": m.pend.helper, "minutes": m.pend.mins}
	}
	n := len(m.log)
	from := 0
	if n > 50 {
		from = n - 50
	}
	out["log"] = m.log[from:]
	return out
}

// Answer resolves the on-screen request.
func (m *Manager) Answer(id string, ok bool) bool {
	m.mu.Lock()
	p := m.pend
	m.mu.Unlock()
	if p == nil || p.id != id {
		return false
	}
	select {
	case p.ch <- ok:
	default:
	}
	return true
}

// End stops the current session.
func (m *Manager) End() {
	m.mu.Lock()
	had := m.session != nil
	m.session = nil
	m.turn = nil
	m.mu.Unlock()
	if had {
		m.emit(Event{Type: "assist_end"})
	}
}

func (m *Manager) active() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil && time.Now().Before(m.session.Until) {
		return m.session
	}
	return nil
}

func (m *Manager) request(ctx context.Context, helper string, mins int) (*Session, error) {
	if mins <= 0 {
		mins = config.Get().Assist.Minutes
	}
	if mins > 480 {
		mins = 480
	}
	if helper == "" {
		helper = "helper"
	}
	m.mu.Lock()
	if m.pend != nil {
		m.mu.Unlock()
		return nil, errors.New("a request is already waiting on screen")
	}
	p := &pending{id: fmt.Sprint(time.Now().UnixNano()), helper: helper, mins: mins, ch: make(chan bool, 1)}
	m.pend = p
	m.mu.Unlock()
	m.emit(Event{Type: "assist_request", Data: map[string]any{"id": p.id, "helper": helper, "minutes": mins}})
	defer func() {
		m.mu.Lock()
		if m.pend == p {
			m.pend = nil
		}
		m.mu.Unlock()
	}()
	select {
	case ok := <-p.ch:
		if !ok {
			m.emit(Event{Type: "assist_declined"})
			return nil, errors.New("the user declined")
		}
	case <-time.After(3 * time.Minute):
		m.emit(Event{Type: "assist_declined"})
		return nil, errors.New("no answer on screen within 3 minutes")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s := &Session{ID: base64.RawURLEncoding.EncodeToString([]byte(p.id + helper))[:16], Helper: helper, Started: time.Now(), Until: time.Now().Add(time.Duration(mins) * time.Minute)}
	m.mu.Lock()
	m.session = s
	m.turn = &guard.Turn{ID: "assist-" + s.ID, Chat: "assist"}
	m.log = nil
	m.mu.Unlock()
	m.emit(Event{Type: "assist_start", Data: map[string]any{"session": s}})
	return s, nil
}

func (m *Manager) run(ctx context.Context, tool string, args json.RawMessage) (string, []ts.Image, error) {
	s := m.active()
	if s == nil {
		return "", nil, errors.New("no active assistance session")
	}
	m.mu.Lock()
	t := m.turn
	m.mu.Unlock()
	var a any
	json.Unmarshal(args, &a)
	acts := guard.Actions(tool, args)
	m.emit(Event{Type: "assist_cmd", Data: map[string]any{"tool": tool, "action": strings.Join(acts, ","), "args": a}})
	start := time.Now()
	text, imgs, err := m.Guard.Run(ctx, config.Get(), t, tool, args)
	e := Entry{Time: start, Tool: tool, Action: strings.Join(acts, ","), Args: a, Millis: time.Since(start).Milliseconds()}
	if err != nil {
		e.Error, e.Result = true, err.Error()
	} else {
		e.Result = text
		if len(e.Result) > 2000 {
			e.Result = e.Result[:2000] + "…"
		}
	}
	m.mu.Lock()
	m.log = append(m.log, e)
	if m.session != nil {
		m.session.Count++
	}
	m.mu.Unlock()
	m.emit(Event{Type: "assist_result", Data: map[string]any{"tool": tool, "result": e.Result, "error": e.Error, "ms": e.Millis}})
	return text, imgs, err
}

// ---- network side ----

func (m *Manager) auth(r *http.Request) bool {
	pw := config.Get().Assist.Password
	if pw == "" {
		return false
	}
	h := strings.TrimPrefix(r.Header.Get("authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(h), []byte(pw)) == 1
}

func out(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (m *Manager) handler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !m.auth(r) {
				out(w, 401, map[string]any{"error": "bad password"})
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /assist/ping", func(w http.ResponseWriter, r *http.Request) {
		out(w, 200, map[string]any{"ok": true, "app": "apichat", "auth": m.auth(r)})
	})
	mux.HandleFunc("POST /assist/request", wrap(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Helper  string `json:"helper"`
			Minutes int    `json:"minutes"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		s, err := m.request(r.Context(), in.Helper, in.Minutes)
		if err != nil {
			out(w, 403, map[string]any{"error": err.Error()})
			return
		}
		out(w, 200, map[string]any{"session": s, "tools": ts.ToolNames(), "tokensaver": ts.Version()})
	}))
	mux.HandleFunc("GET /assist/status", wrap(func(w http.ResponseWriter, r *http.Request) {
		s := m.active()
		out(w, 200, map[string]any{"active": s != nil, "session": s})
	}))
	mux.HandleFunc("GET /assist/tools", wrap(func(w http.ResponseWriter, r *http.Request) {
		out(w, 200, map[string]any{"tools": ts.ToolDefs()})
	}))
	mux.HandleFunc("POST /assist/run", wrap(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Tool == "" {
			out(w, 400, map[string]any{"error": "need tool and args"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		text, imgs, err := m.run(ctx, in.Tool, in.Args)
		res := map[string]any{"text": text}
		if err != nil {
			res["error"] = err.Error()
			if strings.HasPrefix(err.Error(), "no active") {
				out(w, 403, res)
				return
			}
		}
		var images []string
		for _, im := range imgs {
			images = append(images, "data:"+im.Mime+";base64,"+base64.StdEncoding.EncodeToString(im.Data))
		}
		if len(images) > 0 {
			res["images"] = images
		}
		out(w, 200, res)
	}))
	mux.HandleFunc("POST /assist/end", wrap(func(w http.ResponseWriter, r *http.Request) {
		m.End()
		out(w, 200, map[string]any{"ok": true})
	}))
	return mux
}

// tailscaleIP finds this PC's Tailscale address (100.64.0.0/10).
func tailscaleIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipn.IP.To4()
		if ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return ip4.String()
		}
	}
	return ""
}

// Listen starts the assist endpoint on the Tailscale address (or the
// configured one). It returns "" when assist is off or no address exists.
func (m *Manager) Listen() (string, error) {
	cfg := config.Get().Assist
	if !cfg.Enabled {
		return "", nil
	}
	if cfg.Password == "" {
		return "", errors.New("assist is on but has no password")
	}
	host := cfg.Listen
	if host == "" {
		host = tailscaleIP()
	}
	if host == "" {
		return "", errors.New("no Tailscale address found; install Tailscale or set assist.listen")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, cfg.Port))
	if err != nil {
		return "", err
	}
	m.srv = &http.Server{Handler: m.handler(), ReadHeaderTimeout: 10 * time.Second}
	go m.srv.Serve(ln)
	m.mu.Lock()
	m.addr = ln.Addr().String()
	m.mu.Unlock()
	return m.addr, nil
}

func (m *Manager) Shutdown(ctx context.Context) {
	if m.srv != nil {
		m.srv.Shutdown(ctx)
	}
}

// ---- client side (used by the helper's PC) ----

type Client struct {
	Base     string
	Password string
	HTTP     *http.Client
}

func NewClient(host, password string) *Client {
	if !strings.Contains(host, ":") {
		host += ":47822"
	}
	return &Client{Base: "http://" + host, Password: password, HTTP: &http.Client{Timeout: 11 * time.Minute}}
}

func (c *Client) do(method, path string, body any) (map[string]any, error) {
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+c.Password)
	req.Header.Set("content-type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != 200 {
		if e, _ := m["error"].(string); e != "" {
			return m, errors.New(e)
		}
		return m, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return m, nil
}

func (c *Client) Ping() (map[string]any, error) { return c.do("GET", "/assist/ping", nil) }
func (c *Client) Request(helper string, minutes int) (map[string]any, error) {
	return c.do("POST", "/assist/request", map[string]any{"helper": helper, "minutes": minutes})
}
func (c *Client) Status() (map[string]any, error) { return c.do("GET", "/assist/status", nil) }
func (c *Client) End() (map[string]any, error)    { return c.do("POST", "/assist/end", nil) }
func (c *Client) Run(tool string, args json.RawMessage) (map[string]any, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return c.do("POST", "/assist/run", map[string]any{"tool": tool, "args": args})
}
