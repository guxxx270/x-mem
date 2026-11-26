// Package writer is the only code that changes notes. Everything that wants
// a note written — the propose tool, `x-mem remember`, session capture —
// files a proposal in the inbox; the writer checks it and, holding a single
// lock, writes the files, records exactly what it changed (so the write can
// be undone on its own) and commits.
package writer

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Proposal states.
const (
	Queued   = "queued"   // checked, waiting for the writer
	Review   = "review"   // needs your decision: x-mem inbox approve / reject
	Applied  = "applied"  // written; Run says which write
	Rejected = "rejected" // failed a check; Problems says why
)

// Proposal is a note someone wants written.
type Proposal struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Source  string    `json:"source"` // mcp, cli, capture

	Kind    string `json:"kind"`
	Scope   string `json:"scope,omitempty"`   // personal or project
	Project string `json:"project,omitempty"` // the project layer, when there is one
	Title   string `json:"title"`
	Text    string `json:"text"`
	Quote   string `json:"quote,omitempty"`
	Session string `json:"session,omitempty"`
	QuoteAt string `json:"quote_at,omitempty"`
	Context string `json:"context,omitempty"` // capture: the reply the user answered

	Supersedes  string   `json:"supersedes,omitempty"`
	Relation    string   `json:"relation,omitempty"`
	Line        string   `json:"line,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	Enforcement string   `json:"enforcement,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`

	Status     string   `json:"status"`
	Problems   []string `json:"problems,omitempty"`
	Review     string   `json:"review,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Redacted   []string `json:"redacted,omitempty"`
	Run        string   `json:"run,omitempty"`
	Note       string   `json:"note,omitempty"` // the file written
}

// Inbox is where proposals wait. It lives in the state folder, never in git.
type Inbox struct{ Dir string }

// NewInbox opens the inbox under an x-mem state folder.
func NewInbox(state string) Inbox { return Inbox{Dir: filepath.Join(state, "inbox")} }

// Save writes a proposal atomically.
func (in Inbox) Save(p *Proposal) error {
	if err := os.MkdirAll(in.Dir, 0o700); err != nil {
		return err
	}
	p.Updated = time.Now()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(in.Dir, p.ID+".json")
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads one proposal by id (a unique prefix is enough).
func (in Inbox) Load(id string) (*Proposal, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), ".json")
	path := filepath.Join(in.Dir, id+".json")
	if _, err := os.Stat(path); err != nil {
		matches, _ := filepath.Glob(filepath.Join(in.Dir, id+"*.json"))
		if len(matches) != 1 {
			return nil, fmt.Errorf("inbox 里没有提案 %s", id)
		}
		path = matches[0]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("提案 %s 读不懂：%v", id, err)
	}
	return &p, nil
}

// List returns proposals, oldest first. With no states given, all of them.
func (in Inbox) List(states ...string) []*Proposal {
	files, _ := filepath.Glob(filepath.Join(in.Dir, "*.json"))
	want := map[string]bool{}
	for _, s := range states {
		want[s] = true
	}
	var out []*Proposal
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p Proposal
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		if len(want) == 0 || want[p.Status] {
			out = append(out, &p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// RunID is a time plus four random characters, so two writes in the same
// second never share one (旧记忆库's ids stopped at the second, and undoing
// one write undid the other).
func RunID(now time.Time) string {
	return now.Format("20060102T150405") + "-" + randomChars(4, "0123456789abcdefghijklmnopqrstuvwxyz")
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NoteID is ten Crockford base-32 characters: seven from the time in seconds,
// three random. Sorting ids sorts notes by creation.
func NoteID(now time.Time) string {
	n := now.Unix()
	var b [7]byte
	for i := 6; i >= 0; i-- {
		b[i] = crockford[n%32]
		n /= 32
	}
	return string(b[:]) + randomChars(3, crockford)
}

func randomChars(n int, alphabet string) string {
	out := make([]byte, n)
	for i := range out {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[k.Int64()]
	}
	return string(out)
}
