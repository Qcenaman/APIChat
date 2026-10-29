package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectMemoryAndFiles(t *testing.T) {
	ps := NewProjectStore(t.TempDir())
	p, err := ps.Create("Site redesign", "Use British spelling.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Create("  ", ""); err == nil {
		t.Fatal("empty name should fail")
	}
	out, err := ps.RunProjectTool(p.ID, json.RawMessage(`{"action":"remember","text":"Client prefers dark theme"}`))
	if err != nil || !strings.HasPrefix(out, "remembered") {
		t.Fatalf("remember: %q %v", out, err)
	}
	// exact repeat is not stored twice
	ps.RunProjectTool(p.ID, json.RawMessage(`{"action":"remember","text":"client prefers dark theme"}`))
	got, _ := ps.Get(p.ID)
	if len(got.Memory) != 1 {
		t.Fatalf("want 1 note, got %d", len(got.Memory))
	}
	if _, err := ps.AddFile(p.ID, `C:\evil\..\brief.md`, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	fs := ps.Files(p.ID)
	if len(fs) != 1 || fs[0].Name != "brief.md" {
		t.Fatalf("files: %+v", fs)
	}
	if err := ps.RemoveFile(p.ID, "../project.json"); err == nil {
		t.Fatal("path escape should fail")
	}
	txt := ps.PromptText(p.ID)
	for _, want := range []string{"Site redesign", "British spelling", "brief.md", "dark theme"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, txt)
		}
	}
	n, err := ps.Forget(p.ID, nil, "dark")
	if err != nil || n != 1 {
		t.Fatalf("forget: %d %v", n, err)
	}
	if err := ps.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if len(ps.List()) != 0 {
		t.Fatal("project should be gone")
	}
}

func TestChatKeepsProjectID(t *testing.T) {
	s := NewStore(t.TempDir())
	c := s.Create("", "", "proj1")
	got, err := s.Get(c.ID)
	if err != nil || got.ProjectID != "proj1" {
		t.Fatalf("got %+v %v", got, err)
	}
	if h := s.List(); len(h) != 1 || h[0].ProjectID != "proj1" {
		t.Fatalf("list: %+v", h)
	}
}
