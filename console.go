package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qcenaman/apichat/internal/config"
)

func installPath() string {
	name := "apichat"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(config.Root(), name)
}

func copyPlain(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func install() {
	target := installPath()
	exe, _ := os.Executable()
	if err := os.MkdirAll(config.Root(), 0o755); err != nil {
		fmt.Println("Couldn't create", config.Root()+":", err)
		return
	}
	if !strings.EqualFold(filepath.Clean(exe), filepath.Clean(target)) {
		if _, err := os.Stat(target); err == nil {
			os.Remove(target + ".old")
			if err := os.Rename(target, target+".old"); err != nil {
				fmt.Println("Couldn't replace the installed copy (is it running?):", err)
				return
			}
		}
		if err := copyPlain(exe, target); err != nil {
			fmt.Println("Couldn't copy the app:", err)
			return
		}
		fmt.Println("Installed to", target)
	}
	cfg := config.Load()
	if _, err := os.Stat(config.Path()); err != nil {
		config.Save(cfg)
	}
	if runtime.GOOS == "windows" {
		cmd := filepath.Join(config.Root(), "Open APIChat.cmd")
		os.WriteFile(cmd, []byte("@echo off\r\nstart \"APIChat\" /min \""+target+"\"\r\n"), 0o644)
		fmt.Println("Shortcut:", cmd)
	}
	fmt.Println("Settings:", config.Path())
	fmt.Println("TokenSaver data:", cfg.TokenSaverRoot)
	fmt.Println("\nRun it, then open Settings in the window to paste the API key.")
}

func uninstall() {
	target := installPath()
	os.Remove(filepath.Join(config.Root(), "Open APIChat.cmd"))
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		fmt.Println("Couldn't remove", target+":", err)
	}
	fmt.Println("Removed the program. Chats and settings stay in", config.Home())
}

func browserPath() string {
	if b := config.Get().Browser; b != "" {
		return b
	}
	if runtime.GOOS != "windows" {
		for _, n := range []string{"google-chrome", "chromium", "microsoft-edge", "chromium-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		return ""
	}
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	la := os.Getenv("LocalAppData")
	for _, p := range []string{
		filepath.Join(pf86, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(pf86, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(la, "Google", "Chrome", "Application", "chrome.exe"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// openApp opens the UI in its own window when a Chromium browser exists,
// otherwise in the default browser.
func openApp(url string) {
	if b := browserPath(); b != "" {
		profile := filepath.Join(config.Home(), "window")
		cmd := exec.Command(b, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1180,860")
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return
		}
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	} else {
		fmt.Println("Open this in a browser:", url)
	}
}
