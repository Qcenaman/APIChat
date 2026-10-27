package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qcenaman/apichat/internal/api"
)

// Chat is one conversation, with the API messages kept verbatim.
type Chat struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Created      time.Time     `json:"created"`
	Updated      time.Time     `json:"updated"`
	Model        string        `json:"model,omitempty"`
	Instructions string        `json:"instructions,omitempty"`
	Messages     []api.Message `json:"messages"`
	Cost         float64       `json:"cost"`
	Usage        api.Usage     `json:"usage"`
	Turns        int           `json:"turns"`
}

// Header is what the chat list shows.
type Header struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Model   string    `json:"model,omitempty"`
	Cost    float64   `json:"cost"`
	Turns   int       `json:"turns"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func validID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return false
	}
	return true
}

func (s *Store) Create(model, instructions string) *Chat {
	c := &Chat{ID: newID(), Created: time.Now(), Updated: time.Now(), Model: model, Instructions: instructions, Messages: []api.Message{}}
	s.Save(c)
	return c
}

func (s *Store) Get(id string) (*Chat, error) {
	if !validID(id) {
		return nil, errors.New("bad chat id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, errors.New("chat not found")
	}
	var c Chat
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Messages == nil {
		c.Messages = []api.Message{}
	}
	return &c, nil
}

func (s *Store) Save(c *Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Updated = time.Now()
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	p := s.path(c.ID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) Delete(id string) error {
	if !validID(id) {
		return errors.New("bad chat id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.path(id))
}

func (s *Store) List() []Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := os.ReadDir(s.dir)
	var out []Header
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		var h Header
		if json.Unmarshal(b, &h) != nil || h.ID == "" {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}
