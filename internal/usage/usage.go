// Package usage records which notes were handed to an agent and when: what
// the prompt hook injected, what a guard showed, what recall returned and
// what read opened. It is evidence for growing skills (experience that keeps
// being needed) and for spotting skills nobody uses. It stays in the state
// folder, never in git, and is capped in size.
package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"x-mem/internal/config"
)

// MaxBytes is where the log is cut back to its newer half.
const MaxBytes = 512 << 10

type entry struct {
	ID  string `json:"id"`
	At  string `json:"at"`  // RFC 3339
	Via string `json:"via"` // prompt, guard, recall, read, start
}

func path(l config.Layers) string { return filepath.Join(l.State, "usage.jsonl") }

// Record notes that these notes were used now. Errors are ignored: usage
// is a hint, never worth failing a hook over.
func Record(l config.Layers, via string, now time.Time, ids ...string) {
	if len(ids) == 0 || l.State == "" {
		return
	}
	os.MkdirAll(l.State, 0o700)
	f, err := os.OpenFile(path(l), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	var b strings.Builder
	at := now.UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		data, _ := json.Marshal(entry{ID: id, At: at, Via: via})
		b.Write(data)
		b.WriteByte('\n')
	}
	f.WriteString(b.String())
	st, _ := f.Stat()
	f.Close()
	if st != nil && st.Size() > MaxBytes {
		trim(path(l))
	}
}

// trim keeps the newer half of the log.
func trim(p string) {
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	cut := len(data) / 2
	if i := strings.IndexByte(string(data[cut:]), '\n'); i >= 0 {
		cut += i + 1
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data[cut:], 0o600) == nil {
		os.Rename(tmp, p)
	}
}

// Use is how often one note was used.
type Use struct {
	Times int    // occasions
	Days  int    // distinct days
	Last  string // YYYY-MM-DD
}

// Since reads the log from a moment on.
func Since(l config.Layers, since time.Time) map[string]*Use {
	out := map[string]*Use{}
	f, err := os.Open(path(l))
	if err != nil {
		return out
	}
	defer f.Close()
	days := map[string]map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	from := since.UTC().Format(time.RFC3339)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID == "" || e.At < from {
			continue
		}
		u := out[e.ID]
		if u == nil {
			u = &Use{}
			out[e.ID] = u
			days[e.ID] = map[string]bool{}
		}
		u.Times++
		day := e.At
		if len(day) > 10 {
			day = day[:10]
		}
		if !days[e.ID][day] {
			days[e.ID][day] = true
			u.Days++
		}
		if day > u.Last {
			u.Last = day
		}
	}
	return out
}
