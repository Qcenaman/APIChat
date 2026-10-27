package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultBase    = "https://api.anthropic.com"
	defaultVersion = "2023-06-01"
)

// Client talks to the Claude API.
type Client struct {
	Key     string
	Base    string
	Version string
	Bearer  bool
	HTTP    *http.Client

	mu      sync.Mutex
	models  []ModelInfo
	fetched time.Time
}

func New(key string) *Client {
	return &Client{Key: key, Base: defaultBase, Version: defaultVersion, HTTP: &http.Client{Timeout: 0}}
}

func (c *Client) newReq(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	base := c.Base
	if base == "" {
		base = defaultBase
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	ver := c.Version
	if ver == "" {
		ver = defaultVersion
	}
	req.Header.Set("anthropic-version", ver)
	req.Header.Set("content-type", "application/json")
	if c.Bearer {
		req.Header.Set("authorization", "Bearer "+c.Key)
	} else {
		req.Header.Set("x-api-key", c.Key)
	}
	return req, nil
}

func readErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	e := &APIError{Status: resp.StatusCode}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error.Message != "" {
		e.Type, e.Message = body.Error.Type, body.Error.Message
	} else {
		e.Message = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return e
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status == 529 || ae.Status >= 500
	}
	return false
}

// Stream sends a request and streams events to fn. It retries before the
// first byte on transient errors.
func (c *Client) Stream(ctx context.Context, req Request, fn func(Event)) (*Result, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			d := time.Duration(1<<uint(attempt-1)) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		hr, err := c.newReq(ctx, "POST", "/v1/messages", body)
		if err != nil {
			return nil, err
		}
		hr.Header.Set("accept", "text/event-stream")
		resp, err := c.HTTP.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			continue
		}
		if resp.StatusCode != 200 {
			err := readErr(resp)
			resp.Body.Close()
			if retryable(err) {
				last = err
				continue
			}
			return nil, err
		}
		res, started, err := parseStream(resp.Body, fn)
		resp.Body.Close()
		if err != nil && !started && retryable(err) {
			last = err
			continue
		}
		return res, err
	}
	return nil, last
}

type sseEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	ContentBlock Block           `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Usage        json.RawMessage `json:"usage"`
	Error        struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type rawUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Creation   struct {
		M5 int `json:"ephemeral_5m_input_tokens"`
		H1 int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Server struct {
		Web int `json:"web_search_requests"`
	} `json:"server_tool_use"`
	Details struct {
		Thinking int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func mergeUsage(u *Usage, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var r rawUsage
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	if r.Input > 0 {
		u.Input = r.Input
	}
	if r.Output > 0 {
		u.Output = r.Output
	}
	if r.CacheWrite > 0 {
		u.CacheWrite = r.CacheWrite
	}
	if r.Creation.H1 > 0 {
		u.CacheWrite1h = r.Creation.H1
	}
	if r.CacheRead > 0 {
		u.CacheRead = r.CacheRead
	}
	if r.Server.Web > 0 {
		u.WebSearches = r.Server.Web
	}
	if r.Details.Thinking > 0 {
		u.ThinkingToken = r.Details.Thinking
	}
}

func parseStream(r io.Reader, fn func(Event)) (*Result, bool, error) {
	res := &Result{}
	blocks := map[int]Block{}
	partial := map[int]*strings.Builder{}
	texts := map[int]*strings.Builder{}
	started := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := data.String()
		data.Reset()
		var ev sseEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil
		}
		switch ev.Type {
		case "message_start":
			started = true
			var m struct {
				ID    string          `json:"id"`
				Model string          `json:"model"`
				Usage json.RawMessage `json:"usage"`
			}
			json.Unmarshal(ev.Message, &m)
			res.ID, res.Model = m.ID, m.Model
			mergeUsage(&res.Usage, m.Usage)
		case "content_block_start":
			b := ev.ContentBlock
			if b == nil {
				b = Block{}
			}
			blocks[ev.Index] = b
			switch b.Type() {
			case "tool_use", "server_tool_use":
				partial[ev.Index] = &strings.Builder{}
			case "text":
				sb := &strings.Builder{}
				sb.WriteString(b.Str("text"))
				texts[ev.Index] = sb
			}
			fn(Event{Kind: "block_start", Index: ev.Index, Block: b})
		case "content_block_delta":
			var d struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				PartialJSON string          `json:"partial_json"`
				Thinking    string          `json:"thinking"`
				Signature   string          `json:"signature"`
				Citation    json.RawMessage `json:"citation"`
			}
			json.Unmarshal(ev.Delta, &d)
			switch d.Type {
			case "text_delta":
				if sb := texts[ev.Index]; sb != nil {
					sb.WriteString(d.Text)
				}
				fn(Event{Kind: "text", Index: ev.Index, Text: d.Text})
			case "input_json_delta":
				if sb := partial[ev.Index]; sb != nil {
					sb.WriteString(d.PartialJSON)
				}
			case "thinking_delta":
				if b := blocks[ev.Index]; b != nil {
					b["thinking"] = b.Str("thinking") + d.Thinking
				}
				fn(Event{Kind: "thinking", Index: ev.Index, Text: d.Thinking})
			case "signature_delta":
				if b := blocks[ev.Index]; b != nil {
					b["signature"] = b.Str("signature") + d.Signature
				}
			case "citations_delta":
				if b := blocks[ev.Index]; b != nil && len(d.Citation) > 0 {
					var c any
					if json.Unmarshal(d.Citation, &c) == nil {
						arr, _ := b["citations"].([]any)
						b["citations"] = append(arr, c)
					}
				}
			}
		case "content_block_stop":
			b := blocks[ev.Index]
			if b == nil {
				break
			}
			if sb := partial[ev.Index]; sb != nil {
				var in any = map[string]any{}
				s := strings.TrimSpace(sb.String())
				if s != "" {
					json.Unmarshal([]byte(s), &in)
				}
				b["input"] = in
			}
			if sb := texts[ev.Index]; sb != nil {
				b["text"] = sb.String()
			}
			fn(Event{Kind: "block_stop", Index: ev.Index, Block: b})
		case "message_delta":
			var d struct {
				StopReason string `json:"stop_reason"`
			}
			json.Unmarshal(ev.Delta, &d)
			if d.StopReason != "" {
				res.StopReason = d.StopReason
			}
			mergeUsage(&res.Usage, ev.Usage)
		case "error":
			return &APIError{Status: 0, Type: ev.Error.Type, Message: ev.Error.Message}
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, started, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return nil, started, err
	}
	if err := sc.Err(); err != nil {
		return nil, started, err
	}
	if !started {
		return nil, false, errors.New("empty response from the API")
	}
	idx := make([]int, 0, len(blocks))
	for i := range blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		res.Content = append(res.Content, blocks[i])
	}
	return res, true, nil
}

// Models lists the models the key can use, cached for an hour.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	c.mu.Lock()
	if len(c.models) > 0 && time.Since(c.fetched) < time.Hour {
		m := c.models
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()
	hr, err := c.newReq(ctx, "GET", "/v1/models?limit=100", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, readErr(resp)
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Line        string `json:"line"`
			MaxTokens   int    `json:"max_tokens"`
			MaxInput    int    `json:"max_input_tokens"`
			Caps        struct {
				Server struct {
					Web struct {
						Supported bool `json:"supported"`
					} `json:"web_search"`
				} `json:"server_tools"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, d := range body.Data {
		out = append(out, ModelInfo{ID: d.ID, DisplayName: d.DisplayName, Line: d.Line, MaxTokens: d.MaxTokens, MaxInput: d.MaxInput, WebSearch: d.Caps.Server.Web.Supported})
	}
	c.mu.Lock()
	c.models, c.fetched = out, time.Now()
	c.mu.Unlock()
	return out, nil
}

// Ping checks the key with a cheap request.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Models(ctx)
	return err
}
