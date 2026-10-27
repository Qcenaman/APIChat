package api

import (
	"encoding/base64"
	"encoding/json"
)

// Block is one content block, kept as a map so server-tool blocks and
// citations round-trip untouched.
type Block map[string]any

// Message is one turn.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

func (b Block) Type() string { s, _ := b["type"].(string); return s }
func (b Block) Str(k string) string {
	s, _ := b[k].(string)
	return s
}

func Text(s string) Block { return Block{"type": "text", "text": s} }

func Image(mime string, data []byte) Block {
	return Block{"type": "image", "source": map[string]any{"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString(data)}}
}

func Document(mime string, data []byte, title string) Block {
	b := Block{"type": "document", "source": map[string]any{"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString(data)}}
	if title != "" {
		b["title"] = title
	}
	return b
}

func TextDocument(text, title string) Block {
	b := Block{"type": "document", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": text}}
	if title != "" {
		b["title"] = title
	}
	return b
}

func ToolResult(id string, content []Block, isErr bool) Block {
	b := Block{"type": "tool_result", "tool_use_id": id}
	if len(content) > 0 {
		b["content"] = content
	}
	if isErr {
		b["is_error"] = true
	}
	return b
}

// Cache marks a block as a prompt-cache breakpoint. ttl is "" (5m) or "1h".
func Cache(b Block, ttl string) Block {
	cc := map[string]any{"type": "ephemeral"}
	if ttl != "" {
		cc["ttl"] = ttl
	}
	b["cache_control"] = cc
	return b
}

// Request is a Messages API call.
type Request struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       []Block           `json:"system,omitempty"`
	Messages     []Message         `json:"messages"`
	Tools        []json.RawMessage `json:"tools,omitempty"`
	ToolChoice   map[string]any    `json:"tool_choice,omitempty"`
	Thinking     map[string]any    `json:"thinking,omitempty"`
	OutputConfig map[string]any    `json:"output_config,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	Stream       bool              `json:"stream,omitempty"`
}

// Usage is what one call consumed.
type Usage struct {
	Input         int `json:"input"`
	Output        int `json:"output"`
	CacheWrite    int `json:"cache_write"`
	CacheWrite1h  int `json:"cache_write_1h"`
	CacheRead     int `json:"cache_read"`
	WebSearches   int `json:"web_searches"`
	ThinkingToken int `json:"thinking"`
}

func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheWrite += o.CacheWrite
	u.CacheWrite1h += o.CacheWrite1h
	u.CacheRead += o.CacheRead
	u.WebSearches += o.WebSearches
	u.ThinkingToken += o.ThinkingToken
}

func (u Usage) TotalInput() int { return u.Input + u.CacheWrite + u.CacheRead }

// Result is a completed assistant turn.
type Result struct {
	ID         string
	Model      string
	Content    []Block
	StopReason string
	Usage      Usage
}

func (r *Result) ToolUses() []Block {
	var out []Block
	for _, b := range r.Content {
		if b.Type() == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// Event is a streaming callback.
type Event struct {
	Kind  string // text, thinking, block_start, block_stop
	Index int
	Text  string
	Block Block
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Type    string
	Message string
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	return e.Message
}

// ModelInfo is one entry from GET /v1/models.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Line        string `json:"line"`
	MaxTokens   int    `json:"max_tokens"`
	MaxInput    int    `json:"max_input_tokens"`
	WebSearch   bool   `json:"web_search"`
}
