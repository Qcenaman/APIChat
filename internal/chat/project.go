package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Project groups chats and gives them shared instructions, a files folder
// and a memory of notes the model keeps across those chats.
type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Instructions string    `json:"instructions,omitempty"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
	Memory       []Note    `json:"memory"`
}

// Note is one remembered fact about a project.
type Note struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Created time.Time `json:"created"`
}

// ProjectFile is one file kept in the project's folder.
type ProjectFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"path"`
}

const (
	maxProjectNotes   = 300
	maxProjectNoteLen = 2000
	// how much of the memory goes into the system prompt
	maxMemoryPromptChars = 24000
)

type ProjectStore struct {
	dir string
	mu  sync.Mutex
}

func NewProjectStore(dir string) *ProjectStore {
	os.MkdirAll(dir, 0o755)
	return &ProjectStore{dir: dir}
}

func (s *ProjectStore) Dir(id string) string       { return filepath.Join(s.dir, id) }
func (s *ProjectStore) FilesDir(id string) string  { return filepath.Join(s.dir, id, "files") }
func (s *ProjectStore) metaPath(id string) string  { return filepath.Join(s.dir, id, "project.json") }

func (s *ProjectStore) Create(name, instructions string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("give the project a name")
	}
	p := &Project{ID: newID(), Name: name, Instructions: instructions, Created: time.Now(), Updated: time.Now(), Memory: []Note{}}
	os.MkdirAll(s.FilesDir(p.ID), 0o755)
	return p, s.Save(p)
}

func (s *ProjectStore) Get(id string) (*Project, error) {
	if !validID(id) {
		return nil, errors.New("bad project id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}

func (s *ProjectStore) load(id string) (*Project, error) {
	b, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, errors.New("project not found")
	}
	var p Project
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Memory == nil {
		p.Memory = []Note{}
	}
	return &p, nil
}

func (s *ProjectStore) Save(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(p)
}

func (s *ProjectStore) save(p *Project) error {
	p.Updated = time.Now()
	if err := os.MkdirAll(s.Dir(p.ID), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := s.metaPath(p.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *ProjectStore) Delete(id string) error {
	if !validID(id) {
		return errors.New("bad project id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.metaPath(id)); err != nil {
		return errors.New("project not found")
	}
	return os.RemoveAll(s.Dir(id))
}

func (s *ProjectStore) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := os.ReadDir(s.dir)
	var out []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.load(e.Name())
		if err != nil {
			continue
		}
		p.Memory = nil // list stays light
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if out == nil {
		out = []Project{}
	}
	return out
}

// Remember adds a note to the project's memory, de-duplicating exact repeats.
func (s *ProjectStore) Remember(id, text string) (*Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("give text to remember")
	}
	if len(text) > maxProjectNoteLen {
		text = text[:maxProjectNoteLen]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.load(id)
	if err != nil {
		return nil, err
	}
	for _, n := range p.Memory {
		if strings.EqualFold(n.Text, text) {
			return &n, nil
		}
	}
	n := Note{ID: newID(), Text: text, Created: time.Now()}
	p.Memory = append(p.Memory, n)
	if len(p.Memory) > maxProjectNotes {
		p.Memory = p.Memory[len(p.Memory)-maxProjectNotes:]
	}
	return &n, s.save(p)
}

// Forget removes notes by id, or by a text fragment when ids are empty.
func (s *ProjectStore) Forget(id string, ids []string, contains string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.load(id)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, x := range ids {
		want[x] = true
	}
	contains = strings.ToLower(strings.TrimSpace(contains))
	var keep []Note
	removed := 0
	for _, n := range p.Memory {
		if want[n.ID] || (contains != "" && strings.Contains(strings.ToLower(n.Text), contains)) {
			removed++
			continue
		}
		keep = append(keep, n)
	}
	if keep == nil {
		keep = []Note{}
	}
	p.Memory = keep
	if removed == 0 {
		return 0, nil
	}
	return removed, s.save(p)
}

// Files lists the project's files folder (one level, plus subfolders flattened).
func (s *ProjectStore) Files(id string) []ProjectFile {
	if !validID(id) {
		return nil
	}
	root := s.FilesDir(id)
	var out []ProjectFile
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, ProjectFile{Name: filepath.ToSlash(rel), Size: info.Size(), Path: p})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []ProjectFile{}
	}
	return out
}

// AddFile writes data into the project's files folder under name.
func (s *ProjectStore) AddFile(id, name string, data []byte) (string, error) {
	if !validID(id) {
		return "", errors.New("bad project id")
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." {
		return "", errors.New("bad file name")
	}
	dir := s.FilesDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0o644)
}

func (s *ProjectStore) RemoveFile(id, name string) error {
	if !validID(id) {
		return errors.New("bad project id")
	}
	name = filepath.Clean(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
		return errors.New("bad file name")
	}
	p := filepath.Join(s.FilesDir(id), name)
	if _, err := os.Stat(p); err != nil {
		return errors.New("file not found")
	}
	return os.Remove(p)
}

// PromptText is what a project contributes to the system prompt of its chats.
func (s *ProjectStore) PromptText(id string) string {
	p, err := s.Get(id)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nThis chat belongs to the project \"" + p.Name + "\".")
	if strings.TrimSpace(p.Instructions) != "" {
		sb.WriteString("\nProject instructions:\n" + strings.TrimSpace(p.Instructions))
	}
	files := s.Files(id)
	sb.WriteString("\nProject files folder: " + s.FilesDir(id))
	if len(files) == 0 {
		sb.WriteString(" (empty)")
	} else {
		sb.WriteString("\nFiles in it (read them with the read tool when they matter):")
		for i, f := range files {
			if i >= 60 {
				sb.WriteString("\n- … and more; list the folder to see all")
				break
			}
			sb.WriteString("\n- " + f.Name)
		}
	}
	sb.WriteString("\nProject memory is a list of notes kept across all chats in this project. Use the project tool: `remember` a fact, decision or preference that later chats in this project should know (and nothing else), `forget` to remove a note that is wrong or outdated. Save new memory only when it is durable and not already in the list.")
	if len(p.Memory) == 0 {
		sb.WriteString("\nProject memory: (nothing yet)")
	} else {
		sb.WriteString("\nProject memory:")
		used := 0
		for i := len(p.Memory) - 1; i >= 0; i-- {
			n := p.Memory[i]
			line := "\n- [" + n.ID + "] " + n.Text
			if used+len(line) > maxMemoryPromptChars {
				sb.WriteString("\n- (older notes omitted; forget some to make room)")
				break
			}
			used += len(line)
			sb.WriteString(line)
		}
	}
	return sb.String()
}

// ProjectToolDef is the tool the model uses inside a project chat.
var ProjectToolDef = json.RawMessage(`{
 "name": "project",
 "description": "Memory for the current project, shared by all its chats. remember: save a short durable note (a fact, decision or preference the user stated). forget: remove notes by id or by text. list: show all notes. files: list the project's files folder.",
 "input_schema": {
  "type": "object",
  "properties": {
   "action": {"type": "string", "enum": ["remember", "forget", "list", "files"]},
   "text": {"type": "string", "description": "remember: the note to save"},
   "ids": {"type": "array", "items": {"type": "string"}, "description": "forget: note ids"},
   "contains": {"type": "string", "description": "forget: remove notes containing this text"}
  },
  "required": ["action"]
 }
}`)

// RunProjectTool handles a call of the project tool for project id.
func (s *ProjectStore) RunProjectTool(id string, args json.RawMessage) (string, error) {
	var a struct {
		Action   string   `json:"action"`
		Text     string   `json:"text"`
		IDs      []string `json:"ids"`
		Contains string   `json:"contains"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("bad arguments")
	}
	switch a.Action {
	case "remember":
		n, err := s.Remember(id, a.Text)
		if err != nil {
			return "", err
		}
		return "remembered [" + n.ID + "]", nil
	case "forget":
		if len(a.IDs) == 0 && strings.TrimSpace(a.Contains) == "" {
			return "", errors.New("give ids or contains")
		}
		n, err := s.Forget(id, a.IDs, a.Contains)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "nothing matched", nil
		}
		return "forgot " + itoa(n) + " note(s)", nil
	case "list":
		p, err := s.Get(id)
		if err != nil {
			return "", err
		}
		if len(p.Memory) == 0 {
			return "(no notes)", nil
		}
		var sb strings.Builder
		for _, n := range p.Memory {
			sb.WriteString("[" + n.ID + "] " + n.Text + "\n")
		}
		return sb.String(), nil
	case "files":
		fs := s.Files(id)
		if len(fs) == 0 {
			return "(no files) folder: " + s.FilesDir(id), nil
		}
		var sb strings.Builder
		sb.WriteString("folder: " + s.FilesDir(id) + "\n")
		for _, f := range fs {
			sb.WriteString(f.Name + " (" + itoa(int(f.Size)) + " bytes)\n")
		}
		return sb.String(), nil
	}
	return "", errors.New("unknown action: " + a.Action)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
