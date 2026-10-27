package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/qcenaman/apichat/internal/api"
)

// Guest limits what a guest profile may do.
type Guest struct {
	Folders  []string `json:"folders"`
	ToolsOff []string `json:"tools_off"`
	Confirm  []string `json:"confirm"`
	Elevated bool     `json:"elevated"`
}

// Assist lets a trusted helper run tools on this PC over Tailscale after
// the person accepts a request on screen.
type Assist struct {
	Enabled  bool   `json:"enabled"`
	Listen   string `json:"listen,omitempty"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	Minutes  int    `json:"minutes"`
}

type AutoModels struct {
	Simple string `json:"simple"`
	Normal string `json:"normal"`
	Hard   string `json:"hard"`
}

type Config struct {
	Port               int                  `json:"port"`
	APIKey             string               `json:"api_key"`
	APIBase            string               `json:"api_base,omitempty"`
	APIVersion         string               `json:"api_version,omitempty"`
	Bearer             bool                 `json:"bearer,omitempty"`
	Profile            string               `json:"profile"`
	Budget             float64              `json:"budget"`
	BudgetDay          int                  `json:"budget_day"`
	Model              string               `json:"model"`
	Auto               AutoModels           `json:"auto"`
	Router             string               `json:"router"`
	WebSearch          bool                 `json:"web_search"`
	WebSearchMaxUses   int                  `json:"web_search_max_uses"`
	CacheTTL           string               `json:"cache_ttl"`
	Effort             string               `json:"effort,omitempty"`
	Thinking           string               `json:"thinking,omitempty"`
	MaxTokens          int                  `json:"max_tokens"`
	MaxHistoryChars    int                  `json:"max_history_chars"`
	MaxToolRounds      int                  `json:"max_tool_rounds"`
	ScreenshotsPerTurn int                  `json:"screenshots_per_turn"`
	TokenSaverRoot     string               `json:"tokensaver_root"`
	SystemPrompt       string               `json:"system_prompt,omitempty"`
	Browser            string               `json:"browser,omitempty"`
	UserName           string               `json:"user_name,omitempty"`
	Guest              Guest                `json:"guest"`
	Assist             Assist               `json:"assist"`
	Prices             map[string]api.Price `json:"prices,omitempty"`
	Schema             int                  `json:"schema"`
}

const currentSchema = 2

var (
	mu   sync.Mutex
	cur  Config
	path string
)

// Root is the folder APIChat keeps everything in.
func Root() string {
	if v := os.Getenv("APICHAT_ROOT"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		// an existing install wins, otherwise C:\Claude Apps, otherwise AppData
		for _, p := range []string{`D:\Claude Apps\APIChat`, `C:\Claude Apps\APIChat`} {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				return p
			}
		}
		if writable(`C:\Claude Apps\APIChat`) {
			return `C:\Claude Apps\APIChat`
		}
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "APIChat")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".apichat")
}

// writable reports whether the folder exists or can be created.
func writable(p string) bool {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return true
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return false
	}
	return true
}

func Home() string { return filepath.Join(Root(), "data") }
func Path() string { return filepath.Join(Home(), "config.json") }

// Exists reports whether a config file is already on disk.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

func Defaults() Config {
	c := Config{
		Port:               47821,
		Profile:            "owner",
		Budget:             200,
		BudgetDay:          1,
		Model:              "auto",
		Auto:               AutoModels{Simple: "claude-haiku-5-5", Normal: "claude-sonnet-5-5", Hard: "claude-opus-5-5"},
		Router:             "claude-haiku-5-5",
		WebSearch:          false,
		WebSearchMaxUses:   5,
		CacheTTL:           "1h",
		MaxTokens:          16000,
		MaxHistoryChars:    600000,
		MaxToolRounds:      40,
		ScreenshotsPerTurn: 3,
		Assist:             Assist{Port: 47822, Minutes: 60},
		Schema:             currentSchema,
		Guest: Guest{
			ToolsOff: []string{"system.kill", "saved"},
			Confirm:  []string{"window.do", "window.clipboard", "browser.click", "browser.fill", "browser.press", "run", "run_job.delete", "run_job.move", "run_job.sync", "run_job.rename", "office.edit", "rag.forget", "rag.config"},
		},
	}
	if runtime.GOOS == "windows" {
		if fi, err := os.Stat(`D:\Claude Apps\TokenSaver`); err == nil && fi.IsDir() {
			c.TokenSaverRoot = `D:\Claude Apps\TokenSaver`
		}
	}
	if c.TokenSaverRoot == "" {
		c.TokenSaverRoot = filepath.Join(Root(), "tokensaver")
	}
	return c
}

// Load reads the config file, filling defaults.
func Load() Config {
	mu.Lock()
	defer mu.Unlock()
	path = Path()
	cur = Defaults()
	migrate := false
	if b, err := os.ReadFile(path); err == nil {
		cur.Schema = 0
		json.Unmarshal(b, &cur)
		if cur.Schema < currentSchema {
			// schema 2: API web search became off by default
			if cur.Schema < 2 {
				cur.WebSearch = false
			}
			cur.Schema = currentSchema
			migrate = true
		}
	}
	if cur.Assist.Port <= 0 {
		cur.Assist.Port = 47822
	}
	if cur.Assist.Minutes <= 0 {
		cur.Assist.Minutes = 60
	}
	if cur.Port <= 0 {
		cur.Port = 47821
	}
	if cur.MaxTokens <= 0 {
		cur.MaxTokens = 16000
	}
	if cur.MaxToolRounds <= 0 {
		cur.MaxToolRounds = 40
	}
	if cur.MaxHistoryChars <= 0 {
		cur.MaxHistoryChars = 600000
	}
	if cur.Profile != "guest" {
		cur.Profile = "owner"
	}
	if cur.Model == "" {
		cur.Model = "auto"
	}
	if migrate {
		mu.Unlock()
		Save(cur)
		mu.Lock()
	}
	return cur
}

func Get() Config {
	mu.Lock()
	defer mu.Unlock()
	return cur
}

// Save writes the config atomically and makes it current.
func Save(c Config) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	cur = c
	return nil
}

// Update applies fn to the current config and saves it.
func Update(fn func(*Config)) (Config, error) {
	c := Get()
	fn(&c)
	return c, Save(c)
}

// Public is the config without secrets, for the UI.
func (c Config) Public() map[string]any {
	b, _ := json.Marshal(c)
	m := map[string]any{}
	json.Unmarshal(b, &m)
	m["api_key"] = ""
	m["has_key"] = c.APIKey != ""
	if a, ok := m["assist"].(map[string]any); ok {
		a["has_password"] = c.Assist.Password != ""
		a["password"] = ""
	}
	m["root"] = Root()
	m["config_path"] = Path()
	return m
}
