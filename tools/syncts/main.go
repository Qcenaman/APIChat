// syncts copies TokenSaver's Go sources into internal/ts as package ts.
//
//	go run ./tools/syncts <path to TokenSaver checkout>
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pkgRe     = regexp.MustCompile(`(?m)^package main\b`)
	versionRe = regexp.MustCompile(`const appVersion = "([^"]+)"`)
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: syncts <tokensaver dir>")
		os.Exit(2)
	}
	src := os.Args[1]
	dst := filepath.Join("internal", "ts")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fail(err)
	}
	mainSrc, err := os.ReadFile(filepath.Join(src, "main.go"))
	if err != nil {
		fail(err)
	}
	m := versionRe.FindSubmatch(mainSrc)
	if m == nil {
		fail(fmt.Errorf("appVersion not found in main.go"))
	}
	version := string(m[1])

	keep := map[string]bool{"bridge.go": true, "version.go": true}
	entries, err := os.ReadDir(src)
	if err != nil {
		fail(err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "main.go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			fail(err)
		}
		if !pkgRe.Match(b) {
			fail(fmt.Errorf("%s: no 'package main' clause", name))
		}
		b = pkgRe.ReplaceAll(b, []byte("package ts"))
		b = append([]byte("// Code synced from TokenSaver by tools/syncts; do not edit.\n"), b...)
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			fail(err)
		}
		keep[name] = true
		n++
	}
	old, _ := os.ReadDir(dst)
	for _, e := range old {
		if !keep[e.Name()] && strings.HasSuffix(e.Name(), ".go") {
			os.Remove(filepath.Join(dst, e.Name()))
		}
	}
	var vb bytes.Buffer
	fmt.Fprintf(&vb, "// Code synced from TokenSaver by tools/syncts; do not edit.\npackage ts\n\nconst appVersion = %q\n", version)
	if err := os.WriteFile(filepath.Join(dst, "version.go"), vb.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("synced %d files, TokenSaver %s\n", n, version)
}

func fail(err error) {
	fmt.Println("syncts:", err)
	os.Exit(1)
}
