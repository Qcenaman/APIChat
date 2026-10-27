package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/qcenaman/apichat/internal/api"
	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
	"github.com/qcenaman/apichat/internal/ts"
	"github.com/qcenaman/apichat/internal/usage"
)

// Event goes to the UI over SSE.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Attachment is a file the person added to a message.
type Attachment struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Data   []byte `json:"data"`
	Inline bool   `json:"inline"`
}

type pendingConfirm struct {
	ch chan bool
}

type live struct {
	cancel   context.CancelFunc
	turn     *guard.Turn
	text     strings.Builder
	thinking strings.Builder
	status   string
	tool     map[string]any
	model    string
	cost     float64
}

type Engine struct {
	Store     *Store
	Guard     *guard.Guard
	Log       *usage.Log
	UploadDir string
	Client    func() *api.Client

	mu       sync.Mutex
	lives    map[string]*live
	subs     map[string]map[chan Event]bool
	confirms map[string]*pendingConfirm
	turns    map[string]*guard.Turn
}

func NewEngine(store *Store, g *guard.Guard, log *usage.Log, uploadDir string, client func() *api.Client) *Engine {
	e := &Engine{Store: store, Guard: g, Log: log, UploadDir: uploadDir, Client: client,
		lives: map[string]*live{}, subs: map[string]map[chan Event]bool{}, confirms: map[string]*pendingConfirm{}, turns: map[string]*guard.Turn{}}
	g.Confirm = e.confirm
	os.MkdirAll(uploadDir, 0o755)
	return e
}

// Subscribe returns a channel of events for a chat, plus a snapshot of any
// turn in progress.
func (e *Engine) Subscribe(chatID string) (chan Event, func()) {
	ch := make(chan Event, 256)
	e.mu.Lock()
	if e.subs[chatID] == nil {
		e.subs[chatID] = map[chan Event]bool{}
	}
	e.subs[chatID][ch] = true
	if l := e.lives[chatID]; l != nil {
		snap := map[string]any{"text": l.text.String(), "thinking": l.thinking.String(), "status": l.status, "model": l.model}
		if l.tool != nil {
			snap["tool"] = l.tool
		}
		ch <- Event{Type: "snapshot", Data: snap}
	}
	if l := e.lives[chatID]; l != nil && l.tool != nil {
		if cf, ok := l.tool["confirm"].(map[string]any); ok {
			ch <- Event{Type: "confirm", Data: cf}
		}
	}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if m := e.subs[chatID]; m != nil {
			delete(m, ch)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) emit(chatID string, ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs[chatID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (e *Engine) Busy(chatID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lives[chatID] != nil
}

func (e *Engine) Stop(chatID string) {
	e.mu.Lock()
	l := e.lives[chatID]
	e.mu.Unlock()
	if l != nil {
		l.cancel()
	}
}

// Resolve answers a confirmation request.
func (e *Engine) Resolve(id string, ok bool) bool {
	e.mu.Lock()
	p := e.confirms[id]
	e.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case p.ch <- ok:
	default:
	}
	return true
}

func (e *Engine) confirm(ctx context.Context, t *guard.Turn, tool, action string, args json.RawMessage) (bool, error) {
	id := t.Chat + ":" + t.ID + ":" + fmt.Sprint(time.Now().UnixNano())
	p := &pendingConfirm{ch: make(chan bool, 1)}
	var a any
	json.Unmarshal(args, &a)
	data := map[string]any{"id": id, "tool": tool, "action": action, "args": a}
	e.mu.Lock()
	e.confirms[id] = p
	if l := e.lives[t.Chat]; l != nil {
		if l.tool == nil {
			l.tool = map[string]any{}
		}
		l.tool["confirm"] = data
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.confirms, id)
		if l := e.lives[t.Chat]; l != nil && l.tool != nil {
			delete(l.tool, "confirm")
		}
		e.mu.Unlock()
	}()
	e.emit(t.Chat, Event{Type: "confirm", Data: data})
	select {
	case ok := <-p.ch:
		e.emit(t.Chat, Event{Type: "confirm_done", Data: map[string]any{"id": id, "ok": ok}})
		return ok, nil
	case <-time.After(10 * time.Minute):
		e.emit(t.Chat, Event{Type: "confirm_done", Data: map[string]any{"id": id, "ok": false}})
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// RestoreClipboard restores the clipboard a turn overwrote.
func (e *Engine) RestoreClipboard(chatID string) error {
	e.mu.Lock()
	t := e.turns[chatID]
	e.mu.Unlock()
	return guard.RestoreClipboard(t)
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._ \-()]+`)

func (e *Engine) saveUpload(chatID string, a Attachment) (string, error) {
	dir := filepath.Join(e.UploadDir, chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := safeName.ReplaceAllString(filepath.Base(a.Name), "_")
	if name == "" || name == "." {
		name = "file"
	}
	p := filepath.Join(dir, name)
	for i := 2; fileExists(p); i++ {
		ext := filepath.Ext(name)
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
	}
	return p, os.WriteFile(p, a.Data, 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var imageMimes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

func textLike(mime, name string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".json", ".log", ".py", ".go", ".js", ".ts", ".html", ".css", ".xml", ".yaml", ".yml", ".ini", ".cfg", ".bat", ".ps1", ".sh", ".toml":
		return true
	}
	return false
}

// userBlocks turns text and attachments into message content.
func (e *Engine) userBlocks(chatID, text string, atts []Attachment) ([]api.Message, error) {
	var blocks []api.Block
	var notes []string
	for _, a := range atts {
		if len(a.Data) == 0 {
			continue
		}
		mime := strings.ToLower(a.Mime)
		p, err := e.saveUpload(chatID, a)
		if err != nil {
			return nil, err
		}
		switch {
		case imageMimes[mime]:
			blocks = append(blocks, api.Image(mime, a.Data))
			notes = append(notes, fmt.Sprintf("Image %q is also saved at %s", a.Name, p))
		case mime == "application/pdf" && a.Inline:
			blocks = append(blocks, api.Document(mime, a.Data, a.Name))
			notes = append(notes, fmt.Sprintf("PDF %q is also saved at %s", a.Name, p))
		case textLike(mime, a.Name) && a.Inline && len(a.Data) < 200_000:
			blocks = append(blocks, api.TextDocument(string(a.Data), a.Name))
			notes = append(notes, fmt.Sprintf("File %q is also saved at %s", a.Name, p))
		default:
			notes = append(notes, fmt.Sprintf("Attached file %q saved at %s (%d bytes); read it with TokenSaver (read, office, media, local_ai) as needed", a.Name, p, len(a.Data)))
		}
	}
	msg := text
	if len(notes) > 0 {
		msg = strings.TrimSpace(text + "\n\n[" + strings.Join(notes, "\n") + "]")
	}
	if strings.TrimSpace(msg) == "" {
		msg = "(see attached)"
	}
	blocks = append(blocks, api.Text(msg))
	return []api.Message{{Role: "user", Content: blocks}}, nil
}

// Send starts a turn. It returns once the user message is stored.
func (e *Engine) Send(chatID, text string, atts []Attachment) error {
	if e.Busy(chatID) {
		return errors.New("this chat is still working; stop it first")
	}
	c, err := e.Store.Get(chatID)
	if err != nil {
		return err
	}
	cfg := config.Get()
	if cfg.APIKey == "" {
		return errors.New("no API key set; open Settings")
	}
	msgs, err := e.userBlocks(chatID, text, atts)
	if err != nil {
		return err
	}
	c.Messages = append(c.Messages, msgs...)
	c.Turns++
	if err := e.Store.Save(c); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &guard.Turn{ID: fmt.Sprint(c.Turns), Chat: chatID}
	l := &live{cancel: cancel, turn: t, status: "starting"}
	e.mu.Lock()
	e.lives[chatID] = l
	e.turns[chatID] = t
	e.mu.Unlock()
	e.emit(chatID, Event{Type: "user", Data: map[string]any{"message": msgs[0]}})
	go e.run(ctx, c, l, text)
	return nil
}

func (e *Engine) finish(chatID string) {
	e.mu.Lock()
	l := e.lives[chatID]
	delete(e.lives, chatID)
	e.mu.Unlock()
	if l != nil {
		l.cancel()
	}
}

func (e *Engine) setStatus(c *Chat, l *live, s string) {
	e.mu.Lock()
	l.status = s
	e.mu.Unlock()
	e.emit(c.ID, Event{Type: "status", Data: map[string]any{"text": s}})
}

func (e *Engine) run(ctx context.Context, c *Chat, l *live, userText string) {
	defer e.finish(c.ID)
	cfg := config.Get()
	client := e.Client()
	model := c.Model
	if model == "" {
		model = cfg.Model
	}
	if model == "auto" {
		e.setStatus(c, l, "choosing model")
		model = e.route(ctx, client, cfg, userText, c)
	}
	e.mu.Lock()
	l.model = model
	e.mu.Unlock()
	e.emit(c.ID, Event{Type: "model", Data: map[string]any{"model": model}})

	tools := e.tools(cfg)
	system := e.system(cfg, c)
	fail := func(err error) {
		if errors.Is(err, context.Canceled) {
			e.emit(c.ID, Event{Type: "stopped"})
			return
		}
		e.emit(c.ID, Event{Type: "error", Data: map[string]any{"message": err.Error()}})
	}
	for round := 0; round < cfg.MaxToolRounds; round++ {
		if ctx.Err() != nil {
			fail(ctx.Err())
			return
		}
		e.setStatus(c, l, "thinking")
		msgs := trimHistory(c.Messages, cfg.MaxHistoryChars)
		req := api.Request{Model: model, MaxTokens: cfg.MaxTokens, System: system, Messages: withCache(msgs), Tools: tools}
		if cfg.Thinking == "adaptive" {
			req.Thinking = map[string]any{"type": "adaptive"}
		}
		if cfg.Effort != "" {
			req.OutputConfig = map[string]any{"effort": cfg.Effort}
		}
		e.mu.Lock()
		l.text.Reset()
		l.thinking.Reset()
		l.tool = nil
		e.mu.Unlock()
		res, err := client.Stream(ctx, req, func(ev api.Event) {
			switch ev.Kind {
			case "text":
				e.mu.Lock()
				l.text.WriteString(ev.Text)
				e.mu.Unlock()
				e.emit(c.ID, Event{Type: "text", Data: map[string]any{"text": ev.Text}})
			case "thinking":
				e.mu.Lock()
				l.thinking.WriteString(ev.Text)
				e.mu.Unlock()
				e.emit(c.ID, Event{Type: "thinking", Data: map[string]any{"text": ev.Text}})
			case "block_start":
				if ev.Block.Type() == "server_tool_use" || ev.Block.Type() == "tool_use" {
					e.setStatus(c, l, "preparing "+ev.Block.Str("name"))
				}
			case "block_stop":
				if ev.Block.Type() == "text" {
					e.emit(c.ID, Event{Type: "text_done", Data: map[string]any{"block": ev.Block}})
				}
			}
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				e.savePartial(c, l)
			}
			fail(err)
			return
		}
		if res.Model == "" {
			res.Model = model
		}
		cost := api.Cost(res.Model, res.Usage, cfg.Prices)
		c.Messages = append(c.Messages, api.Message{Role: "assistant", Content: res.Content})
		c.Cost += cost
		c.Usage.Add(res.Usage)
		l.cost += cost
		e.Store.Save(c)
		e.Log.Add(usage.Record{Kind: "api", Chat: c.ID, Model: res.Model, Input: res.Usage.Input, Output: res.Usage.Output, CacheWrite: res.Usage.CacheWrite, CacheRead: res.Usage.CacheRead, Searches: res.Usage.WebSearches, Cost: cost, Profile: cfg.Profile})
		e.emit(c.ID, Event{Type: "assistant", Data: map[string]any{"message": c.Messages[len(c.Messages)-1], "usage": res.Usage, "cost": cost, "chat_cost": c.Cost, "turn_cost": l.cost}})

		switch res.StopReason {
		case "tool_use":
			var results []api.Block
			for _, tu := range res.ToolUses() {
				if ctx.Err() != nil {
					results = append(results, api.ToolResult(tu.Str("id"), []api.Block{api.Text("stopped by the user")}, true))
					continue
				}
				results = append(results, e.callTool(ctx, cfg, c, l, tu))
			}
			c.Messages = append(c.Messages, api.Message{Role: "user", Content: results})
			e.Store.Save(c)
			if ctx.Err() != nil {
				fail(ctx.Err())
				return
			}
			continue
		case "pause_turn":
			continue
		case "max_tokens":
			e.emit(c.ID, Event{Type: "notice", Data: map[string]any{"text": "The reply hit the output limit."}})
		}
		break
	}
	if c.Title == "" {
		e.setStatus(c, l, "naming chat")
		if t := e.title(ctx, client, cfg, c); t != "" {
			c.Title = t
			e.Store.Save(c)
			e.emit(c.ID, Event{Type: "title", Data: map[string]any{"title": t}})
		}
	}
	done := map[string]any{"chat_cost": c.Cost, "turn_cost": l.cost}
	if l.turn.ClipboardSet && l.turn.ClipboardBefore != nil {
		done["clipboard_changed"] = true
	}
	e.emit(c.ID, Event{Type: "done", Data: done})
}

// savePartial keeps the text streamed before a stop as an assistant message.
func (e *Engine) savePartial(c *Chat, l *live) {
	e.mu.Lock()
	txt := strings.TrimSpace(l.text.String())
	e.mu.Unlock()
	if txt == "" {
		return
	}
	c.Messages = append(c.Messages, api.Message{Role: "assistant", Content: []api.Block{api.Text(txt)}})
	e.Store.Save(c)
}

func (e *Engine) callTool(ctx context.Context, cfg config.Config, c *Chat, l *live, tu api.Block) api.Block {
	id, name := tu.Str("id"), tu.Str("name")
	args, _ := json.Marshal(tu["input"])
	e.mu.Lock()
	l.tool = map[string]any{"id": id, "name": name, "input": tu["input"]}
	l.status = "running " + name
	e.mu.Unlock()
	e.emit(c.ID, Event{Type: "tool_start", Data: map[string]any{"id": id, "name": name, "input": tu["input"]}})
	text, imgs, err := e.Guard.Run(ctx, cfg, l.turn, name, args)
	var content []api.Block
	isErr := err != nil
	if isErr {
		text = err.Error()
	}
	if text == "" && len(imgs) == 0 {
		text = "(no output)"
	}
	if text != "" {
		content = append(content, api.Text(text))
	}
	var thumbs []string
	for _, im := range imgs {
		content = append(content, api.Image(im.Mime, im.Data))
		thumbs = append(thumbs, "data:"+im.Mime+";base64,"+base64.StdEncoding.EncodeToString(im.Data))
	}
	e.mu.Lock()
	l.tool = nil
	e.mu.Unlock()
	e.emit(c.ID, Event{Type: "tool_result", Data: map[string]any{"id": id, "name": name, "text": text, "images": thumbs, "error": isErr}})
	return api.ToolResult(id, content, isErr)
}

func (e *Engine) tools(cfg config.Config) []json.RawMessage {
	defs := ts.ToolDefs()
	out := make([]json.RawMessage, 0, len(defs)+1)
	for _, d := range defs {
		if cfg.Profile == "guest" && toolOff(cfg, d) {
			continue
		}
		out = append(out, d)
	}
	if n := len(out); n > 0 {
		out[n-1] = addCache(out[n-1], cfg.CacheTTL)
	}
	if cfg.WebSearch {
		max := cfg.WebSearchMaxUses
		if max <= 0 {
			max = 5
		}
		b, _ := json.Marshal(map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": max})
		out = append(out, b)
	}
	return out
}

func toolOff(cfg config.Config, def json.RawMessage) bool {
	var m struct {
		Name string `json:"name"`
	}
	json.Unmarshal(def, &m)
	for _, p := range cfg.Guest.ToolsOff {
		if p == m.Name {
			return true
		}
	}
	return false
}

func addCache(def json.RawMessage, ttl string) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(def, &m) != nil {
		return def
	}
	cc := map[string]any{"type": "ephemeral"}
	if ttl == "1h" {
		cc["ttl"] = "1h"
	}
	m["cache_control"] = cc
	b, _ := json.Marshal(m)
	return b
}

const baseSystem = `You are APIChat, a desktop assistant running on the user's own Windows PC. You have TokenSaver, a set of tools that run locally and return short results, so you can read files, run commands, use the screen, the browser, Office files and more, exactly like a desktop assistant.

Working rules:
- Use TokenSaver whenever it can do the job instead of asking the user to do it. Act; don't just describe.
- Spend as few tokens as possible: window text or ocr before screenshot; screenshot with a question before a real screenshot; local_ai to read or sort many files; read with lines, grep or pages rather than whole files; web (free) before web_search (paid, $0.01 per search).
- Before clicking or typing in other apps, check the window text first, and re-read positions if time passed. Set the clipboard only when the task needs it.
- Dry-run deletes, renames and overwrites, and say what a destructive action will do before running it.
- Long work returns a job id; poll job status rather than repeating the call.
- Keep replies short and concrete. Use Markdown. Don't restate what the tools printed; summarize what matters.`

func (e *Engine) system(cfg config.Config, c *Chat) []api.Block {
	var sb strings.Builder
	sb.WriteString(baseSystem)
	sb.WriteString("\n\nTokenSaver notes: ")
	sb.WriteString(ts.Instructions())
	if cfg.Profile == "guest" {
		sb.WriteString("\n\nThis user has a guest profile. File access is limited to: ")
		if len(cfg.Guest.Folders) == 0 {
			sb.WriteString("(no folders yet; ask the owner to add some in Settings)")
		} else {
			sb.WriteString(strings.Join(cfg.Guest.Folders, "; "))
		}
		sb.WriteString(". Uploaded files live under " + e.UploadDir + ". Some actions ask the user for permission first; if refused, don't retry them.")
	}
	if cfg.SystemPrompt != "" {
		sb.WriteString("\n\n" + cfg.SystemPrompt)
	}
	blocks := []api.Block{api.Cache(api.Text(sb.String()), cfg.CacheTTL)}
	var dyn strings.Builder
	dyn.WriteString("Today is " + time.Now().Format("Monday, 2 January 2006") + ".")
	if cfg.UserName != "" {
		dyn.WriteString(" The user's name is " + cfg.UserName + ".")
	}
	if c.Instructions != "" {
		dyn.WriteString("\n\nInstructions for this chat:\n" + c.Instructions)
	}
	blocks = append(blocks, api.Text(dyn.String()))
	return blocks
}

// withCache marks the last block of the last message as a cache breakpoint.
func withCache(msgs []api.Message) []api.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]api.Message, len(msgs))
	copy(out, msgs)
	last := out[len(out)-1]
	if len(last.Content) == 0 {
		return out
	}
	blocks := make([]api.Block, len(last.Content))
	copy(blocks, last.Content)
	lb := api.Block{}
	for k, v := range blocks[len(blocks)-1] {
		lb[k] = v
	}
	if lb.Type() != "thinking" && lb.Type() != "redacted_thinking" {
		api.Cache(lb, "")
	}
	blocks[len(blocks)-1] = lb
	out[len(out)-1] = api.Message{Role: last.Role, Content: blocks}
	return out
}

func msgSize(m api.Message) int {
	b, _ := json.Marshal(m)
	return len(b)
}

func startsTurn(m api.Message) bool {
	if m.Role != "user" || len(m.Content) == 0 {
		return false
	}
	return m.Content[0].Type() != "tool_result"
}

// trimHistory drops the oldest turns when the conversation is too long,
// never splitting a tool_use from its result.
func trimHistory(msgs []api.Message, max int) []api.Message {
	if max <= 0 {
		return msgs
	}
	sizes := make([]int, len(msgs))
	total := 0
	for i, m := range msgs {
		sizes[i] = msgSize(m)
		total += sizes[i]
	}
	start := 0
	for total > max {
		next := -1
		for i := start + 1; i < len(msgs); i++ {
			if startsTurn(msgs[i]) {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		for i := start; i < next; i++ {
			total -= sizes[i]
		}
		start = next
	}
	return msgs[start:]
}

func lastUserText(c *Chat) string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Role == "user" {
			for _, b := range m.Content {
				if b.Type() == "text" {
					return b.Str("text")
				}
			}
		}
	}
	return ""
}

func (e *Engine) route(ctx context.Context, client *api.Client, cfg config.Config, text string, c *Chat) string {
	a := cfg.Auto
	if a.Simple == "" {
		a.Simple = "claude-haiku-5-5"
	}
	if a.Normal == "" {
		a.Normal = "claude-sonnet-5-5"
	}
	if a.Hard == "" {
		a.Hard = "claude-opus-5-5"
	}
	router := cfg.Router
	if router == "" {
		router = a.Simple
	}
	if len(text) > 3000 {
		text = text[:3000]
	}
	hist := ""
	if n := len(c.Messages); n > 1 {
		hist = fmt.Sprintf(" The conversation already has %d messages.", n)
	}
	req := api.Request{Model: router, MaxTokens: 5,
		System:   []api.Block{api.Text("Rate how capable a model the next request needs. Reply with one digit only: 1 = quick chat, a short factual question, a tiny edit, or a simple file lookup; 2 = normal work: multi-step tasks on the PC, writing, summarizing, code changes, research; 3 = hard: tricky debugging, long careful analysis, subtle reasoning, high-stakes or irreversible actions." + hist)},
		Messages: []api.Message{{Role: "user", Content: []api.Block{api.Text("Request:\n" + text)}}}}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := client.Stream(rctx, req, func(api.Event) {})
	if err != nil {
		return a.Normal
	}
	cost := api.Cost(res.Model, res.Usage, cfg.Prices)
	c.Cost += cost
	e.Log.Add(usage.Record{Kind: "api", Chat: c.ID, Model: res.Model, Input: res.Usage.Input, Output: res.Usage.Output, CacheWrite: res.Usage.CacheWrite, CacheRead: res.Usage.CacheRead, Cost: cost, Profile: cfg.Profile})
	out := ""
	for _, b := range res.Content {
		out += b.Str("text")
	}
	switch {
	case strings.Contains(out, "1"):
		return a.Simple
	case strings.Contains(out, "3"):
		return a.Hard
	}
	return a.Normal
}

func (e *Engine) title(ctx context.Context, client *api.Client, cfg config.Config, c *Chat) string {
	model := cfg.Router
	if model == "" {
		model = "claude-haiku-5-5"
	}
	u := lastUserText(c)
	if len(u) > 1500 {
		u = u[:1500]
	}
	var a string
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "assistant" {
			for _, b := range c.Messages[i].Content {
				if b.Type() == "text" {
					a = b.Str("text")
				}
			}
			break
		}
	}
	if len(a) > 800 {
		a = a[:800]
	}
	req := api.Request{Model: model, MaxTokens: 20,
		System:   []api.Block{api.Text("Write a title for this chat: 2 to 6 words, no quotes, no trailing period, same language as the user.")},
		Messages: []api.Message{{Role: "user", Content: []api.Block{api.Text("User: " + u + "\n\nAssistant: " + a)}}}}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := client.Stream(rctx, req, func(api.Event) {})
	if err != nil {
		return ""
	}
	cost := api.Cost(res.Model, res.Usage, cfg.Prices)
	c.Cost += cost
	e.Log.Add(usage.Record{Kind: "api", Chat: c.ID, Model: res.Model, Input: res.Usage.Input, Output: res.Usage.Output, Cost: cost, Profile: cfg.Profile})
	t := ""
	for _, b := range res.Content {
		t += b.Str("text")
	}
	t = strings.Trim(strings.TrimSpace(strings.Split(t, "\n")[0]), `"'.`)
	if len(t) > 60 {
		t = t[:60]
	}
	return t
}
