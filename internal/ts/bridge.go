package ts

import (
	"encoding/json"
	"os"
	"strings"
)

// Image is a picture a tool returned alongside its text.
type Image struct {
	Data []byte
	Mime string
}

// Init prepares the embedded TokenSaver engine. root overrides the
// TOKENSAVER_ROOT folder when set.
func Init(root string) {
	if root != "" {
		os.Setenv("TOKENSAVER_ROOT", root)
	}
	initApp()
	resumeNotifier()
}

// Version is the synced TokenSaver version.
func Version() string { return appVersion }

// Root is the folder the engine keeps everything in.
func Root() string { return appRoot() }

// Home is the engine's data folder.
func Home() string { return appHome }

// Instructions is the text TokenSaver gives MCP clients.
func Instructions() string { return serverInstructions }

// ToolDefs returns the tool definitions in Claude API form (name,
// description, input_schema).
func ToolDefs() []json.RawMessage {
	out := make([]json.RawMessage, 0, len(toolDefs))
	for _, d := range toolDefs {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(d, &m); err != nil {
			continue
		}
		if s, ok := m["inputSchema"]; ok {
			m["input_schema"] = s
			delete(m, "inputSchema")
		}
		b, _ := json.Marshal(m)
		out = append(out, b)
	}
	return out
}

// ToolNames lists the tool names in definition order.
func ToolNames() []string {
	var out []string
	for _, d := range toolDefs {
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(d, &m) == nil && m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out
}

// Call runs one tool and returns its text, images and error.
func Call(name string, args json.RawMessage) (string, []Image, error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "null" {
		args = json.RawMessage("{}")
	}
	text, imgs, err := callTool(name, args)
	out := make([]Image, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, Image{Data: im.data, Mime: im.mime})
	}
	return text, out, err
}

// Shutdown waits for running jobs and stops helper processes.
func Shutdown() {
	waitAllJobs()
	embedSrv.shutdown()
}
