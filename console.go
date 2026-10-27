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

// selfSetup copies the program into its folder and makes the shortcut the
// first time it is run from somewhere else. Settings and chats are kept.
func selfSetup() {
	target := installPath()
	exe, _ := os.Executable()
	if strings.EqualFold(filepath.Clean(exe), filepath.Clean(target)) {
		return
	}
	if err := os.MkdirAll(config.Root(), 0o755); err != nil {
		return
	}
	if _, err := os.Stat(target); err == nil {
		os.Remove(target + ".old")
		if err := os.Rename(target, target+".old"); err != nil {
			fmt.Println("Note: the installed copy is in use, running this one instead:", err)
			return
		}
	}
	if err := copyPlain(exe, target); err != nil {
		fmt.Println("Couldn't copy the app to", target+":", err)
		return
	}
	if runtime.GOOS == "windows" {
		cmd := filepath.Join(config.Root(), "Open APIChat.cmd")
		os.WriteFile(cmd, []byte("@echo off\r\nstart \"APIChat\" /min \""+target+"\"\r\n"), 0o644)
	}
	adoptOldData()
	fmt.Println("Installed to", target, "(settings and chats are kept between versions)")
}

// adoptOldData moves the data folder an earlier version kept in AppData
// into the new install folder, so the key and chats carry over.
func adoptOldData() {
	if _, err := os.Stat(config.Path()); err == nil {
		return
	}
	for _, old := range []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "APIChat", "data"),
		`D:\Claude Apps\APIChat\data`,
	} {
		if _, err := os.Stat(filepath.Join(old, "config.json")); err != nil {
			continue
		}
		if strings.EqualFold(filepath.Clean(old), filepath.Clean(config.Home())) {
			return
		}
		os.MkdirAll(config.Root(), 0o755)
		os.RemoveAll(config.Home())
		if err := os.Rename(old, config.Home()); err != nil {
			if err := copyDir(old, config.Home()); err != nil {
				fmt.Println("Couldn't move old settings from", old+":", err)
				return
			}
		}
		fmt.Println("Moved settings and chats from", old)
		return
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		return copyPlain(p, t)
	})
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
	existed := config.Exists()
	cfg := config.Load()
	if existed {
		// Keep the API key and every other setting from the previous install;
		// only migrate web search off (it was on by default before 0.1.2).
		cfg.WebSearch = false
		config.Save(cfg)
		fmt.Println("Kept your existing settings" + keyNote(cfg) + " and turned API web search off.")
	} else {
		config.Save(cfg)
	}
	if runtime.GOOS == "windows" {
		cmd := filepath.Join(config.Root(), "Open APIChat.cmd")
		os.WriteFile(cmd, []byte("@echo off\r\nstart \"APIChat\" /min \""+target+"\"\r\n"), 0o644)
		fmt.Println("Shortcut:", cmd)
	}
	fmt.Println("Settings:", config.Path())
	fmt.Println("TokenSaver data:", cfg.TokenSaverRoot)
	if !existed {
		fmt.Println("\nRun it, then open Settings in the window to paste the API key.")
	} else {
		fmt.Println("\nRun it. Your API key and settings are unchanged.")
	}
}

func keyNote(cfg config.Config) string {
	if cfg.APIKey != "" {
		return " (API key kept)"
	}
	return ""
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
