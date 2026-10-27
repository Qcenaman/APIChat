package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qcenaman/apichat/internal/api"
	"github.com/qcenaman/apichat/internal/assist"
	"github.com/qcenaman/apichat/internal/chat"
	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/ts"
	"github.com/qcenaman/apichat/internal/usage"
	"github.com/qcenaman/apichat/ui"
)

type Server struct {
	Engine  *chat.Engine
	Store   *chat.Store
	Log     *usage.Log
	Client  func() *api.Client
	Version string
	Assist  *assist.Manager

	token string
	index []byte
	srv   *http.Server
}

func New(engine *chat.Engine, store *chat.Store, log *usage.Log, client func() *api.Client, version string) *Server {
	b := make([]byte, 16)
	rand.Read(b)
	s := &Server{Engine: engine, Store: store, Log: log, Client: client, Version: version, token: hex.EncodeToString(b)}
	raw, _ := fs.ReadFile(ui.Files, "index.html")
	s.index = []byte(strings.ReplaceAll(string(raw), "{{TOKEN}}", s.token))
	return s
}

func (s *Server) Token() string { return s.token }

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func bad(w http.ResponseWriter, code int, err error) {
	jsonOut(w, code, map[string]any{"error": err.Error()})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := r.Header.Get("x-token")
		if t == "" {
			t = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
			bad(w, 401, errors.New("not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static := http.FileServer(http.FS(ui.Files))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		w.Write(s.index)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"ok": true, "version": s.Version})
	})
	apiMux.HandleFunc("GET /api/state", s.state)
	apiMux.HandleFunc("GET /api/chats", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, s.Store.List()) })
	apiMux.HandleFunc("POST /api/chats", s.createChat)
	apiMux.HandleFunc("GET /api/chats/{id}", s.getChat)
	apiMux.HandleFunc("PATCH /api/chats/{id}", s.patchChat)
	apiMux.HandleFunc("DELETE /api/chats/{id}", s.deleteChat)
	apiMux.HandleFunc("POST /api/chats/{id}/send", s.send)
	apiMux.HandleFunc("POST /api/chats/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		s.Engine.Stop(r.PathValue("id"))
		jsonOut(w, 200, map[string]any{"ok": true})
	})
	apiMux.HandleFunc("GET /api/chats/{id}/events", s.events)
	apiMux.HandleFunc("POST /api/chats/{id}/clipboard/restore", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Engine.RestoreClipboard(r.PathValue("id")); err != nil {
			bad(w, 400, err)
			return
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	})
	apiMux.HandleFunc("POST /api/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		jsonOut(w, 200, map[string]any{"ok": s.Engine.Resolve(in.ID, in.OK)})
	})
	apiMux.HandleFunc("GET /api/usage", s.usage)
	apiMux.HandleFunc("GET /api/assist/status", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, s.Assist.Status()) })
	apiMux.HandleFunc("POST /api/assist/answer", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		jsonOut(w, 200, map[string]any{"ok": s.Assist.Answer(in.ID, in.OK)})
	})
	apiMux.HandleFunc("POST /api/assist/end", func(w http.ResponseWriter, r *http.Request) {
		s.Assist.End()
		jsonOut(w, 200, map[string]any{"ok": true})
	})
	apiMux.HandleFunc("GET /api/assist/events", s.assistEvents)
	apiMux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, config.Get().Public()) })
	apiMux.HandleFunc("PUT /api/settings", s.putSettings)
	apiMux.HandleFunc("GET /api/models", s.models)
	apiMux.HandleFunc("GET /api/tokensaver/config", s.tsConfigGet)
	apiMux.HandleFunc("PUT /api/tokensaver/config", s.tsConfigPut)
	apiMux.HandleFunc("GET /api/tokensaver/tools", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"version": ts.Version(), "root": ts.Root(), "tools": ts.ToolNames()})
	})
	mux.Handle("/api/", s.auth(apiMux))
	return mux
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	from := usage.CycleStart(cfg.BudgetDay, time.Now())
	sum := s.Log.Summarize(from)
	jsonOut(w, 200, map[string]any{
		"version":    s.Version,
		"settings":   cfg.Public(),
		"usage":      sum,
		"budget":     cfg.Budget,
		"tokensaver": map[string]any{"version": ts.Version(), "root": ts.Root(), "tools": ts.ToolNames()},
		"assist":     s.Assist.Status(),
		"chats":      s.Store.List(),
	})
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	from := usage.CycleStart(cfg.BudgetDay, time.Now())
	if q := r.URL.Query().Get("days"); q != "" {
		var d int
		fmt.Sscan(q, &d)
		if d > 0 {
			from = time.Now().AddDate(0, 0, -d)
		}
	}
	jsonOut(w, 200, map[string]any{"budget": cfg.Budget, "summary": s.Log.Summarize(from)})
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	jsonOut(w, 200, s.Store.Create(in.Model, in.Instructions))
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		bad(w, 404, err)
		return
	}
	jsonOut(w, 200, map[string]any{"chat": c, "busy": s.Engine.Busy(c.ID)})
}

func (s *Server) patchChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.Get(r.PathValue("id"))
	if err != nil {
		bad(w, 404, err)
		return
	}
	var in struct {
		Title        *string `json:"title"`
		Model        *string `json:"model"`
		Instructions *string `json:"instructions"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if in.Title != nil {
		c.Title = strings.TrimSpace(*in.Title)
	}
	if in.Model != nil {
		c.Model = *in.Model
	}
	if in.Instructions != nil {
		c.Instructions = *in.Instructions
	}
	s.Store.Save(c)
	jsonOut(w, 200, c)
}

func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Engine.Stop(id)
	if err := s.Store.Delete(id); err != nil {
		bad(w, 404, err)
		return
	}
	os.RemoveAll(filepath.Join(s.Engine.UploadDir, id))
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseMultipartForm(96 << 20); err != nil {
		bad(w, 400, err)
		return
	}
	text := r.FormValue("text")
	var meta []struct {
		Name   string `json:"name"`
		Inline bool   `json:"inline"`
	}
	json.Unmarshal([]byte(r.FormValue("meta")), &meta)
	var atts []chat.Attachment
	if r.MultipartForm != nil {
		for i, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, 64<<20))
			f.Close()
			if err != nil {
				continue
			}
			a := chat.Attachment{Name: fh.Filename, Mime: fh.Header.Get("content-type"), Data: data}
			if i < len(meta) {
				a.Inline = meta[i].Inline
			}
			if a.Mime == "" || a.Mime == "application/octet-stream" {
				a.Mime = guessMime(fh.Filename)
			}
			atts = append(atts, a)
		}
	}
	if m := r.FormValue("model"); m != "" {
		if c, err := s.Store.Get(id); err == nil && c.Model != m {
			c.Model = m
			s.Store.Save(c)
		}
	}
	if err := s.Engine.Send(id, text, atts); err != nil {
		bad(w, 400, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func guessMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md", ".csv", ".log":
		return "text/plain"
	}
	return "application/octet-stream"
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fl, ok := w.(http.Flusher)
	if !ok {
		bad(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(200)
	ch, cancel := s.Engine.Subscribe(id)
	defer cancel()
	write := func(ev chat.Event) bool {
		b, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	write(chat.Event{Type: "hello", Data: map[string]any{"busy": s.Engine.Busy(id)}})
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !write(ev) {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) assistEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		bad(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(200)
	ch, cancel := s.Assist.Subscribe()
	defer cancel()
	b, _ := json.Marshal(assist.Event{Type: "assist_status", Data: s.Assist.Status()})
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		bad(w, 400, err)
		return
	}
	cfg := config.Get()
	cur, _ := json.Marshal(cfg)
	merged := map[string]json.RawMessage{}
	json.Unmarshal(cur, &merged)
	for k, v := range in {
		if k == "assist" {
			var a map[string]json.RawMessage
			json.Unmarshal(v, &a)
			var pw string
			json.Unmarshal(a["password"], &pw)
			if strings.TrimSpace(pw) == "" {
				b, _ := json.Marshal(cfg.Assist.Password)
				a["password"] = b
			}
			delete(a, "has_password")
			v, _ = json.Marshal(a)
		}
		if k == "api_key" {
			var s string
			json.Unmarshal(v, &s)
			if strings.TrimSpace(s) == "" {
				continue
			}
			v, _ = json.Marshal(strings.TrimSpace(s))
		}
		merged[k] = v
	}
	mb, _ := json.Marshal(merged)
	var next config.Config
	if err := json.Unmarshal(mb, &next); err != nil {
		bad(w, 400, err)
		return
	}
	if next.Profile != "guest" {
		next.Profile = "owner"
	}
	if err := config.Save(next); err != nil {
		bad(w, 500, err)
		return
	}
	jsonOut(w, 200, next.Public())
}

var fallbackModels = []api.ModelInfo{
	{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5"},
	{ID: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5"},
	{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5"},
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cfg := config.Get()
	var list []api.ModelInfo
	var err error
	if cfg.APIKey != "" {
		list, err = s.Client().Models(ctx)
	}
	if len(list) == 0 {
		list = fallbackModels
	}
	out := map[string]any{"models": list}
	if err != nil {
		out["error"] = err.Error()
	}
	jsonOut(w, 200, out)
}

func (s *Server) tsConfigGet(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(ts.Home(), "config.json")
	b, _ := os.ReadFile(p)
	if len(b) == 0 {
		b = []byte("{\n}\n")
	}
	jsonOut(w, 200, map[string]any{"path": p, "text": string(b)})
}

func (s *Server) tsConfigPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	var v map[string]any
	if err := json.Unmarshal([]byte(in.Text), &v); err != nil {
		bad(w, 400, fmt.Errorf("not valid JSON: %v", err))
		return
	}
	p := filepath.Join(ts.Home(), "config.json")
	os.MkdirAll(ts.Home(), 0o755)
	if err := os.WriteFile(p, []byte(in.Text), 0o644); err != nil {
		bad(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "note": "restart APIChat to apply"})
}

// Listen starts serving on 127.0.0.1:port and returns the address.
func (s *Server) Listen(port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return "http://" + ln.Addr().String(), nil
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.srv != nil {
		s.srv.Shutdown(ctx)
	}
}
