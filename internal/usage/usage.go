package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is one API call or tool call.
type Record struct {
	Time       time.Time `json:"t"`
	Kind       string    `json:"kind"` // api | tool
	Chat       string    `json:"chat,omitempty"`
	Model      string    `json:"model,omitempty"`
	Input      int       `json:"in,omitempty"`
	Output     int       `json:"out,omitempty"`
	CacheWrite int       `json:"cw,omitempty"`
	CacheRead  int       `json:"cr,omitempty"`
	Searches   int       `json:"ws,omitempty"`
	Cost       float64   `json:"cost,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Action     string    `json:"action,omitempty"`
	BytesIn    int       `json:"bin,omitempty"`
	BytesOut   int       `json:"bout,omitempty"`
	Millis     int64     `json:"ms,omitempty"`
	Error      bool      `json:"err,omitempty"`
	Profile    string    `json:"profile,omitempty"`
}

type Log struct {
	dir string
	mu  sync.Mutex
}

func New(dir string) *Log {
	os.MkdirAll(dir, 0o755)
	return &Log{dir: dir}
}

func (l *Log) file(t time.Time) string {
	return filepath.Join(l.dir, t.UTC().Format("2006-01")+".jsonl")
}

func (l *Log) Add(r Record) {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.file(r.Time), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// Since reads every record at or after t.
func (l *Log) Since(t time.Time) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Record
	for m := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(time.Now().UTC()); m = m.AddDate(0, 1, 0) {
		f, err := os.Open(l.file(m))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 4<<20)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) == nil && !r.Time.Before(t) {
				out = append(out, r)
			}
		}
		f.Close()
	}
	return out
}

// CycleStart is the start of the current billing cycle for a day of month.
func CycleStart(day int, now time.Time) time.Time {
	if day < 1 || day > 28 {
		day = 1
	}
	s := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, now.Location())
	if s.After(now) {
		s = s.AddDate(0, -1, 0)
	}
	return s
}

type Summary struct {
	From      time.Time          `json:"from"`
	To        time.Time          `json:"to"`
	Cost      float64            `json:"cost"`
	Calls     int                `json:"calls"`
	Input     int                `json:"input"`
	Output    int                `json:"output"`
	CacheRead int                `json:"cache_read"`
	Searches  int                `json:"searches"`
	ToolCalls int                `json:"tool_calls"`
	ByModel   map[string]float64 `json:"by_model"`
	ByTool    map[string]int     `json:"by_tool"`
	ByDay     []DayCost          `json:"by_day"`
	ByChat    map[string]float64 `json:"by_chat"`
}

type DayCost struct {
	Day  string  `json:"day"`
	Cost float64 `json:"cost"`
}

func (l *Log) Summarize(from time.Time) Summary {
	s := Summary{From: from, To: time.Now(), ByModel: map[string]float64{}, ByTool: map[string]int{}, ByChat: map[string]float64{}}
	days := map[string]float64{}
	for _, r := range l.Since(from) {
		switch r.Kind {
		case "api":
			s.Cost += r.Cost
			s.Calls++
			s.Input += r.Input + r.CacheWrite + r.CacheRead
			s.Output += r.Output
			s.CacheRead += r.CacheRead
			s.Searches += r.Searches
			s.ByModel[r.Model] += r.Cost
			s.ByChat[r.Chat] += r.Cost
			days[r.Time.Local().Format("2006-01-02")] += r.Cost
		case "tool":
			s.ToolCalls++
			s.ByTool[r.Tool]++
		}
	}
	for d, c := range days {
		s.ByDay = append(s.ByDay, DayCost{d, c})
	}
	sort.Slice(s.ByDay, func(i, j int) bool { return s.ByDay[i].Day < s.ByDay[j].Day })
	return s
}
