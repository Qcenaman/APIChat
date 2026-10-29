package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
	"github.com/qcenaman/apichat/internal/ts"
)

// bridge lets APIChatHTML (a single HTML file opened in a browser) run
// TokenSaver tools on this PC. The page talks to the Claude API itself and
// only sends tool calls here. Requests carry the bridge key from Settings.
type bridge struct {
	mu    sync.Mutex
	turns map[string]*turnState
}

type turnState struct {
	t    *guard.Turn
	seen time.Time
}

func newBridge() *bridge { return &bridge{turns: map[string]*turnState{}} }

func (b *bridge) turn(chat, id string) *guard.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := chat + ":" + id
	if st := b.turns[key]; st != nil {
		st.seen = time.Now()
		return st.t
	}
	if len(b.turns) > 200 {
		var keys []string
		for k := range b.turns {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return b.turns[keys[i]].seen.Before(b.turns[keys[j]].seen) })
		for _, k := range keys[:100] {
			delete(b.turns, k)
		}
	}
	t := &guard.Turn{ID: id, Chat: chat}
	b.turns[key] = &turnState{t: t, seen: time.Now()}
	return t
}

func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("access-control-allow-origin", "*")
	h.Set("access-control-allow-methods", "GET, POST, OPTIONS")
	h.Set("access-control-allow-headers", "content-type, x-bridge-key")
	h.Set("access-control-max-age", "600")
}

// bridgeAuth checks the bridge key and answers CORS preflights.
func bridgeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		key := config.Get().BridgeKey
		got := r.Header.Get("x-bridge-key")
		if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			bad(w, 401, errors.New("bad bridge key; copy it from APIChat Settings"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bridgeHandler() http.Handler {
	b := newBridge()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bridge/info", func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()
		defs := ts.ToolDefs()
		tools := make([]json.RawMessage, 0, len(defs))
		for _, d := range defs {
			var m struct {
				Name string `json:"name"`
			}
			json.Unmarshal(d, &m)
			if cfg.Profile == "guest" && listed(cfg.Guest.ToolsOff, m.Name) {
				continue
			}
			tools = append(tools, d)
		}
		jsonOut(w, 200, map[string]any{
			"app": "apichat", "version": s.Version, "tokensaver": ts.Version(),
			"profile": cfg.Profile, "instructions": ts.Instructions(), "tools": tools,
			"upload_dir":    filepath.Join(config.Home(), "uploads", "html"),
			"guest_folders": cfg.Guest.Folders,
		})
	})
	mux.HandleFunc("POST /bridge/call", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Chat     string          `json:"chat"`
			Turn     string          `json:"turn"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
			Approved bool            `json:"approved"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&in); err != nil {
			bad(w, 400, err)
			return
		}
		if in.Name == "" {
			bad(w, 400, errors.New("name is required"))
			return
		}
		if in.Chat == "" {
			in.Chat = "html"
		}
		if in.Turn == "" {
			in.Turn = "0"
		}
		cfg := config.Get()
		t := b.turn(in.Chat, in.Turn)
		d := s.Engine.Guard.Check(cfg, in.Name, in.Input)
		if d.Deny == "" && d.Confirm && !in.Approved {
			jsonOut(w, 202, map[string]any{"confirm": map[string]any{"tool": in.Name, "action": strings.Join(d.Actions, ", ")}})
			return
		}
		t.Preapproved = in.Approved
		text, imgs, err := s.Engine.Guard.Run(r.Context(), cfg, t, in.Name, in.Input)
		t.Preapproved = false
		out := map[string]any{"text": text, "error": err != nil, "clipboard_changed": t.ClipboardSet && t.ClipboardBefore != nil}
		if err != nil {
			out["text"] = err.Error()
		}
		var images []map[string]string
		for _, im := range imgs {
			images = append(images, map[string]string{"mime": im.Mime, "data": base64.StdEncoding.EncodeToString(im.Data)})
		}
		out["images"] = images
		jsonOut(w, 200, out)
	})
	mux.HandleFunc("POST /bridge/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(256 << 20); err != nil {
			bad(w, 400, err)
			return
		}
		chat := r.FormValue("chat")
		if chat == "" {
			chat = "html"
		}
		dir := filepath.Join(config.Home(), "uploads", "html", safeName.ReplaceAllString(chat, "_"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			bad(w, 500, err)
			return
		}
		var files []map[string]any
		for _, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				bad(w, 400, err)
				return
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				bad(w, 400, err)
				return
			}
			name := safeName.ReplaceAllString(filepath.Base(fh.Filename), "_")
			if name == "" || name == "." {
				name = "file"
			}
			p := filepath.Join(dir, name)
			for i := 2; exists(p); i++ {
				ext := filepath.Ext(name)
				p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				bad(w, 500, err)
				return
			}
			files = append(files, map[string]any{"name": fh.Filename, "path": p, "size": len(data)})
		}
		jsonOut(w, 200, map[string]any{"files": files})
	})
	mux.HandleFunc("POST /bridge/clipboard/restore", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Chat string `json:"chat"`
			Turn string `json:"turn"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Chat == "" {
			in.Chat = "html"
		}
		if err := guard.RestoreClipboard(b.turn(in.Chat, in.Turn)); err != nil {
			bad(w, 400, err)
			return
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	})
	return bridgeAuth(mux)
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._ \-()]+`)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func listed(list []string, name string) bool {
	for _, p := range list {
		if p == name {
			return true
		}
	}
	return false
}
