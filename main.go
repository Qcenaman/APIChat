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

	"github.com/qcenaman/apichat/internal/api"
	"github.com/qcenaman/apichat/internal/chat"
	"github.com/qcenaman/apichat/internal/config"
	"github.com/qcenaman/apichat/internal/guard"
	"github.com/qcenaman/apichat/internal/server"
	"github.com/qcenaman/apichat/internal/ts"
	"github.com/qcenaman/apichat/internal/usage"
)

const appVersion = "0.1.1"

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
	default:
		fmt.Println("usage: apichat [start|serve|install|uninstall|version]")
		os.Exit(2)
	}
}

func start(openWindow bool) {
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
	srv := server.New(engine, store, log, client, appVersion)
	addr, err := srv.Listen(cfg.Port)
	if err != nil {
		fmt.Printf("Can't listen on port %d: %v\n", cfg.Port, err)
		os.Exit(1)
	}
	fmt.Printf("APIChat %s (TokenSaver %s)\n%s\nprofile: %s   data: %s\n", appVersion, ts.Version(), addr+"/", cfg.Profile, config.Home())
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
	ts.Shutdown()
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
