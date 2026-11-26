// Package transcript reads Claude Code's session files
// (~/.claude/projects/<project>/<session>.jsonl): one JSON object per line.
// x-mem only ever needs two things from them: what the user typed, and the
// assistant's text just before it for context.
package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"x-mem/internal/textutil"
)

// Message is one turn's text.
type Message struct {
	Role    string // "user" or "assistant"
	Text    string
	Session string
	Time    string
	Cwd     string
}

// Dir is where Claude Code keeps session files.
func Dir() string {
	if d := os.Getenv("X_MEM_TRANSCRIPTS"); d != "" {
		return d
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "projects")
}

type line struct {
	Type      string          `json:"type"`
	IsMeta    bool            `json:"isMeta"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var (
	reminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	commandRe  = regexp.MustCompile(`^\s*<(command-name|command-message|command-args|local-command-stdout|local-command-stderr|bash-input|bash-stdout|bash-stderr)>`)
)

// ReadFrom reads the complete lines after offset and returns the messages in
// them and the offset to continue from. A half-written last line is left
// for next time.
func ReadFrom(path string, offset int64) ([]Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < offset {
		offset = 0 // the file was replaced
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	r := bufio.NewReaderSize(f, 1<<16)
	var out []Message
	pos := offset
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			break // EOF or a line still being written
		}
		pos += int64(len(raw))
		if m, ok := parse(raw); ok {
			out = append(out, m)
		}
		if err != nil {
			break
		}
	}
	return out, pos, nil
}

func parse(raw []byte) (Message, bool) {
	var l line
	if json.Unmarshal(raw, &l) != nil || l.IsMeta || (l.Type != "user" && l.Type != "assistant") {
		return Message{}, false
	}
	var m msg
	if json.Unmarshal(l.Message, &m) != nil {
		return Message{}, false
	}
	var text string
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		text = s
	} else {
		var blocks []block
		if json.Unmarshal(m.Content, &blocks) != nil {
			return Message{}, false
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	if l.Type == "user" {
		if commandRe.MatchString(text) || strings.HasPrefix(text, "Caveat: ") {
			return Message{}, false
		}
		text = reminderRe.ReplaceAllString(text, "")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, false
	}
	return Message{Role: l.Type, Text: text, Session: l.SessionID, Time: l.Timestamp, Cwd: l.Cwd}, true
}

// Recent lists session files changed within the window, newest first.
func Recent(within time.Duration, max int) []string {
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	cut := time.Now().Add(-within)
	matches, _ := filepath.Glob(filepath.Join(Dir(), "*", "*.jsonl"))
	for _, p := range matches {
		if st, err := os.Stat(p); err == nil && st.ModTime().After(cut) {
			files = append(files, f{p, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var out []string
	for i, x := range files {
		if max > 0 && i >= max {
			break
		}
		out = append(out, x.path)
	}
	return out
}

// Norm keeps letters and digits only, lower-cased, so a quote matches
// whatever punctuation or spacing it was typed with.
func Norm(s string) string {
	var b strings.Builder
	for _, r := range textutil.Normalize(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Found is where a quote was found.
type Found struct {
	Session string // "claude:<session id>"
	Path    string
	Time    string
}

// FindQuote looks for a user message containing quote in the session files
// changed within the window. Only what the user typed counts: a quote that
// appears in a tool result or a web page the agent read is not the user's.
func FindQuote(quote string, within time.Duration) (Found, bool) {
	q := Norm(quote)
	if len([]rune(q)) < 4 {
		return Found{}, false
	}
	for _, p := range Recent(within, 200) {
		msgs, _, err := ReadFrom(p, 0)
		if err != nil {
			continue
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if m.Role == "user" && strings.Contains(Norm(m.Text), q) {
				sid := m.Session
				if sid == "" {
					sid = strings.TrimSuffix(filepath.Base(p), ".jsonl")
				}
				return Found{Session: "claude:" + sid, Path: p, Time: m.Time}, true
			}
		}
	}
	return Found{}, false
}
