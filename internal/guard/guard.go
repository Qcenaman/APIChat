package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/ts"
	"github.com/qcenaman/apichat/internal/usage"
)

// Turn carries per-turn state the guard needs.
type Turn struct {
	ID              string
	Chat            string
	Screenshots     int
	ClipboardBefore *string
	ClipboardSet    bool
	Preapproved     bool // the person already allowed this call (bridge)
}

// Confirmer asks the person whether a tool call may go ahead.
type Confirmer func(ctx context.Context, t *Turn, tool, action string, args json.RawMessage) (bool, error)

type Guard struct {
	Log     *usage.Log
	Confirm Confirmer

	screenMu  sync.Mutex
	browserMu sync.Mutex
}

var absRe = absPattern()
var envRe = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)

func absPattern() *regexp.Regexp {
	if runtime.GOOS == "windows" {
		return regexp.MustCompile(`^([A-Za-z]:[\\/]|\\\\[^\\]|~[\\/]|%[A-Za-z0-9_()]+%)`)
	}
	return regexp.MustCompile(`^(/|~/)`)
}

// Actions lists the action names a call performs, e.g. ["run_job.delete"].
func Actions(tool string, args json.RawMessage) []string {
	var a struct {
		Action   string `json:"action"`
		Elevated bool   `json:"elevated"`
		Steps    []struct {
			Op string `json:"op"`
		} `json:"steps"`
		Saved string  `json:"saved"`
		Text  *string `json:"text"`
	}
	json.Unmarshal(args, &a)
	var out []string
	switch tool {
	case "run_job":
		for _, s := range a.Steps {
			if s.Op != "" {
				out = append(out, tool+"."+s.Op)
			}
		}
		if a.Saved != "" {
			out = append(out, tool+".saved")
		}
	case "run":
		out = append(out, "run")
		if a.Elevated {
			out = append(out, "run.elevated")
		}
	case "window":
		if a.Action == "clipboard" && a.Text == nil {
			out = append(out, "window.clipboard_get")
		} else if a.Action != "" {
			out = append(out, tool+"."+a.Action)
		}
	default:
		if a.Action != "" {
			out = append(out, tool+"."+a.Action)
		}
	}
	if len(out) == 0 {
		out = append(out, tool)
	}
	return out
}

func matches(pattern string, tool string, actions []string) bool {
	if pattern == tool {
		return true
	}
	for _, a := range actions {
		if a == pattern {
			return true
		}
	}
	return false
}

func expand(p string) string {
	p = envRe.ReplaceAllStringFunc(p, func(m string) string {
		if v := os.Getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
	if strings.HasPrefix(p, "~") {
		h, _ := os.UserHomeDir()
		p = h + p[1:]
	}
	return filepath.Clean(strings.ReplaceAll(p, "/", string(filepath.Separator)))
}

func under(p, root string) bool {
	p, root = strings.ToLower(expand(p)), strings.ToLower(expand(root))
	if p == root {
		return true
	}
	return strings.HasPrefix(p, strings.TrimRight(root, `\/`)+string(filepath.Separator))
}

// pathsIn collects strings that look like absolute paths anywhere in args.
func pathsIn(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(strings.Trim(x, `"`))
		if absRe.MatchString(s) && !strings.Contains(s, "://") {
			*out = append(*out, s)
		}
	case []any:
		for _, e := range x {
			pathsIn(e, out)
		}
	case map[string]any:
		for _, e := range x {
			pathsIn(e, out)
		}
	}
}

// Decision is what the guard decided about a call.
type Decision struct {
	Deny    string
	Confirm bool
	Actions []string
}

// Check applies the profile rules.
func (g *Guard) Check(cfg config.Config, tool string, args json.RawMessage) Decision {
	d := Decision{Actions: Actions(tool, args)}
	if cfg.Profile != "guest" {
		return d
	}
	gc := cfg.Guest
	for _, p := range gc.ToolsOff {
		if matches(p, tool, d.Actions) {
			d.Deny = fmt.Sprintf("%s is switched off for this user", p)
			return d
		}
	}
	if !gc.Elevated {
		for _, a := range d.Actions {
			if a == "run.elevated" {
				d.Deny = "admin (elevated) commands are not allowed for this user; run it without elevated"
				return d
			}
		}
	}
	var v any
	json.Unmarshal(args, &v)
	var paths []string
	pathsIn(v, &paths)
	for _, p := range paths {
		ok := under(p, config.Home())
		for _, f := range gc.Folders {
			if under(p, f) {
				ok = true
				break
			}
		}
		if !ok {
			d.Deny = fmt.Sprintf("%s is outside the folders this user may use (%s)", p, strings.Join(gc.Folders, "; "))
			return d
		}
	}
	for _, p := range gc.Confirm {
		if matches(p, tool, d.Actions) {
			d.Confirm = true
			break
		}
	}
	return d
}

func isScreenInput(tool string, actions []string) bool {
	if tool != "window" {
		return false
	}
	for _, a := range actions {
		switch a {
		case "window.do", "window.copy", "window.clipboard", "window.wait":
			return true
		}
	}
	return false
}

// Run checks, confirms, serializes and logs one tool call.
func (g *Guard) Run(ctx context.Context, cfg config.Config, t *Turn, tool string, args json.RawMessage) (string, []ts.Image, error) {
	d := g.Check(cfg, tool, args)
	if d.Deny != "" {
		g.log(cfg, t, tool, d.Actions, args, "", 0, true)
		return "", nil, errors.New("refused: " + d.Deny)
	}
	if d.Confirm && !t.Preapproved && g.Confirm != nil {
		ok, err := g.Confirm(ctx, t, tool, strings.Join(d.Actions, ", "), args)
		if err != nil {
			return "", nil, err
		}
		if !ok {
			g.log(cfg, t, tool, d.Actions, args, "", 0, true)
			return "", nil, errors.New("the user did not allow this action")
		}
	}
	for _, a := range d.Actions {
		if a == "window.screenshot" {
			var q struct {
				Question string `json:"question"`
			}
			json.Unmarshal(args, &q)
			if q.Question == "" {
				if t.Screenshots >= cfg.ScreenshotsPerTurn && cfg.ScreenshotsPerTurn > 0 {
					return "", nil, fmt.Errorf("screenshot limit for this turn reached (%d); use window text, ocr, or screenshot with a question instead", cfg.ScreenshotsPerTurn)
				}
				t.Screenshots++
			}
		}
	}
	if isScreenInput(tool, d.Actions) {
		g.screenMu.Lock()
		defer g.screenMu.Unlock()
		if err := waitIdle(ctx, 15*time.Second); err != nil {
			return "", nil, err
		}
		for _, a := range d.Actions {
			if a == "window.clipboard" && t.ClipboardBefore == nil {
				if prev, _, err := ts.Call("window", json.RawMessage(`{"action":"clipboard","max_chars":30000}`)); err == nil {
					t.ClipboardBefore = &prev
				}
				t.ClipboardSet = true
			}
		}
	}
	if tool == "browser" {
		g.browserMu.Lock()
		defer g.browserMu.Unlock()
	}
	start := time.Now()
	text, imgs, err := ts.Call(tool, args)
	g.log(cfg, t, tool, d.Actions, args, text, time.Since(start), err != nil)
	return text, imgs, err
}

func (g *Guard) log(cfg config.Config, t *Turn, tool string, actions []string, args json.RawMessage, out string, d time.Duration, isErr bool) {
	if g.Log == nil {
		return
	}
	act := ""
	if len(actions) > 0 {
		act = strings.Join(actions, ",")
	}
	g.Log.Add(usage.Record{Kind: "tool", Chat: t.Chat, Tool: tool, Action: act, BytesIn: len(args), BytesOut: len(out), Millis: d.Milliseconds(), Error: isErr, Profile: cfg.Profile})
}

// RestoreClipboard puts back what the clipboard held before this turn changed it.
func RestoreClipboard(t *Turn) error {
	if t == nil || t.ClipboardBefore == nil {
		return errors.New("nothing to restore")
	}
	b, _ := json.Marshal(map[string]any{"action": "clipboard", "text": *t.ClipboardBefore})
	_, _, err := ts.Call("window", b)
	return err
}

// waitIdle waits until the person has not touched mouse or keyboard for a moment.
func waitIdle(ctx context.Context, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		idle := idleFor()
		if idle < 0 || idle >= 1500*time.Millisecond {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the user is using the mouse or keyboard right now; wait and try again, or ask them to pause")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}
