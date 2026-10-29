package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"encoding/json"
	"strings"

	"github.com/qcenaman/apichat/internal/api"
	"github.com/qcenaman/apichat/internal/assist"
	"github.com/qcenaman/apichat/internal/chat"
	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
	"github.com/qcenaman/apichat/internal/server"
	"github.com/qcenaman/apichat/internal/ts"
	"github.com/qcenaman/apichat/internal/usage"
)

const appVersion = "0.2.0"

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "", "start", "open":
		start(true)
	case "serve":
		start(false)
	case "install":
		install()
	case "uninstall":
		uninstall()
	case "version":
		fmt.Println("apichat", appVersion, "(TokenSaver", ts.Version()+")")
	case "assist":
		os.Exit(assistCLI(args[1:]))
	default:
		fmt.Println("usage: apichat [start|serve|install|uninstall|version|assist]")
		os.Exit(2)
	}
}

func start(openWindow bool) {
	selfSetup()
	cfg := config.Load()
	if err := os.MkdirAll(config.Home(), 0o755); err != nil {
		fmt.Println("Can't create", config.Home()+":", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", cfg.Port)
	if alive(url) {
		fmt.Println("APIChat is already running at", url)
		if openWindow {
			openApp(url)
		}
		return
	}
	ts.Init(cfg.TokenSaverRoot)
	log := usage.New(filepath.Join(config.Home(), "usage"))
	store := chat.NewStore(filepath.Join(config.Home(), "chats"))
	g := &guard.Guard{Log: log}
	client := func() *api.Client {
		c := config.Get()
		cl := api.New(c.APIKey)
		if c.APIBase != "" {
			cl.Base = c.APIBase
		}
		if c.APIVersion != "" {
			cl.Version = c.APIVersion
		}
		cl.Bearer = c.Bearer
		return cl
	}
	engine := chat.NewEngine(store, g, log, filepath.Join(config.Home(), "uploads"), client)
	engine.Projects = chat.NewProjectStore(filepath.Join(config.Home(), "projects"))
	srv := server.New(engine, store, log, client, appVersion)
	am := assist.New(g)
	srv.Assist = am
	addr, err := srv.Listen(cfg.Port)
	if err != nil {
		fmt.Printf("Can't listen on port %d: %v\n", cfg.Port, err)
		os.Exit(1)
	}
	fmt.Printf("APIChat %s (TokenSaver %s)\n%s\nprofile: %s   data: %s\n", appVersion, ts.Version(), addr+"/", cfg.Profile, config.Home())
	if aaddr, err := am.Listen(); err != nil {
		fmt.Println("assist:", err)
	} else if aaddr != "" {
		fmt.Println("assist: listening on", aaddr)
	}
	if openWindow {
		openApp(addr + "/")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("stopping...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	am.Shutdown(ctx)
	ts.Shutdown()
}

// assistCLI drives another PC's APIChat from this one:
//
//	apichat assist <host> <password> request [name] [minutes]
//	apichat assist <host> <password> run <tool> '<json args>'
//	apichat assist <host> <password> status|end|ping
func assistCLI(a []string) int {
	if len(a) < 3 {
		fmt.Println("usage: apichat assist <host> <password> ping|request [name] [minutes]|run <tool> <json>|status|end")
		return 2
	}
	c := assist.NewClient(a[0], a[1])
	var res map[string]any
	var err error
	switch a[2] {
	case "ping":
		res, err = c.Ping()
	case "request":
		name, mins := "helper", 0
		if len(a) > 3 {
			name = a[3]
		}
		if len(a) > 4 {
			fmt.Sscan(a[4], &mins)
		}
		res, err = c.Request(name, mins)
	case "run":
		if len(a) < 4 {
			fmt.Println("run needs a tool name")
			return 2
		}
		args := "{}"
		if len(a) > 4 {
			args = strings.Join(a[4:], " ")
		}
		res, err = c.Run(a[3], json.RawMessage(args))
	case "status":
		res, err = c.Status()
	case "end":
		res, err = c.End()
	default:
		fmt.Println("unknown assist command:", a[2])
		return 2
	}
	if err != nil {
		fmt.Println("error:", err)
		if res != nil {
			if t, _ := res["text"].(string); t != "" {
				fmt.Println(t)
			}
		}
		return 1
	}
	if t, ok := res["text"].(string); ok {
		fmt.Println(t)
		if imgs, _ := res["images"].([]any); len(imgs) > 0 {
			fmt.Printf("(%d image(s) returned)\n", len(imgs))
		}
		return 0
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	return 0
}

func alive(url string) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(url + "api/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 401 || resp.StatusCode == 200
}
