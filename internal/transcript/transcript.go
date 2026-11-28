// Package transcript reads the session files of the three agents x-mem
// serves, one JSON object per line each:
//
//   - Claude Code: ~/.claude/projects/<project>/<session>.jsonl
//   - Codex:       ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
//   - Antigravity: ~/.gemini/antigravity*/brain/<id>/.system_generated/logs/transcript.jsonl
//
// x-mem only ever needs two things from them: what the user typed, and the
// assistant's text just before it for context. Context the agents inject
// (environment blocks, AGENTS.md, system reminders) and headless runs
// (`claude -p`, `codex exec`, x-mem's own host runs) are not the user's words.
package transcript

import (
	"bufio"
	"bytes"
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

// Clients x-mem reads.
const (
	Claude      = "claude"
	Codex       = "codex"
	Antigravity = "antigravity"
	DSH         = "dsh"
	Cursor      = "cursor"
	Grok        = "grok"
)

// Message is one turn's text, or ("meta") something about the session.
type Message struct {
	Role     string // "user", "assistant" or "meta"
	Text     string
	Client   string
	Session  string // the agent's own session id
	Time     string
	Cwd      string
	Headless bool // meta: the session is automation, not a person
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// Dir is where Claude Code keeps session files.
func Dir() string {
	if d := os.Getenv("X_MEM_TRANSCRIPTS"); d != "" {
		return d
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home(), ".claude")
	}
	return filepath.Join(base, "projects")
}

// CodexDir is where Codex keeps session files.
func CodexDir() string {
	if d := os.Getenv("X_MEM_CODEX_SESSIONS"); d != "" {
		return d
	}
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		base = filepath.Join(home(), ".codex")
	}
	return filepath.Join(base, "sessions")
}

// AntigravityDirs are Antigravity's app data folders (app, CLI, IDE).
func AntigravityDirs() []string {
	if d := os.Getenv("X_MEM_ANTIGRAVITY_DIRS"); d != "" {
		return filepath.SplitList(d)
	}
	g := filepath.Join(home(), ".gemini")
	return []string{filepath.Join(g, "antigravity"), filepath.Join(g, "antigravity-cli"), filepath.Join(g, "antigravity-ide")}
}

// HookTranscriptsDir is where x-mem logs user prompts for agents whose own
// session files are compressed or binary (DSH, Cursor, Grok CLI).
func HookTranscriptsDir() string {
	h := os.Getenv("X_MEM_HOME")
	if h == "" {
		h = filepath.Join(home(), ".x-mem")
	}
	return filepath.Join(h, "state", "transcripts")
}

// ClientOf guesses which agent wrote a session file from its path.
func ClientOf(path string) string {
	p := filepath.ToSlash(path)
	base := filepath.Base(p)
	switch {
	case strings.Contains(p, "/state/transcripts/"):
		switch {
		case strings.HasPrefix(base, "dsh-") || strings.HasPrefix(base, "dsh_"):
			return DSH
		case strings.HasPrefix(base, "cursor-") || strings.HasPrefix(base, "cursor_"):
			return Cursor
		case strings.HasPrefix(base, "grok-") || strings.HasPrefix(base, "grok_"):
			return Grok
		}
	case strings.Contains(p, "/.system_generated/") || strings.Contains(p, "/brain/"):
		return Antigravity
	case strings.HasPrefix(base, "rollout-") || strings.Contains(p, "/.codex/"):
		return Codex
	}
	return Claude
}

// SessionOf is the session id a file belongs to, from its path.
func SessionOf(path string) string {
	c := ClientOf(path)
	if c == Antigravity {
		// brain/<conversationId>/.system_generated/logs/transcript.jsonl
		return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	}
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if c == DSH || c == Cursor || c == Grok {
		name = strings.TrimPrefix(name, c+"-")
		name = strings.TrimPrefix(name, c+"_")
	}
	return name
}

var (
	reminderRe = regexp.MustCompile(`(?s)<(system-reminder|ide_selection|ide_opened_file)>.*?</(system-reminder|ide_selection|ide_opened_file)>`)
	noiseRe    = regexp.MustCompile(`(?i)^\s*(<(environment_context|user_instructions|system-reminder|command-|local-command|task-notification|turn_aborted|permissions|bash-input|bash-stdout|bash-stderr)|# AGENTS\.md|Caveat: The messages below|This session is being continued)`)
	requestRe  = regexp.MustCompile(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)
	agMetaRe   = regexp.MustCompile(`(?s)<(ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>.*?</(ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>`)
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
			if c := ClientOf(path); c == DSH || c == Cursor || c == Grok {
				m.Client = c
				m.Session = strings.TrimPrefix(strings.TrimPrefix(m.Session, c+"-"), c+"_")
			}
			if m.Session == "" && m.Role != "meta" {
				m.Session = SessionOf(path)
			}
			if m.Role == "user" && strings.Contains(m.Text, HostMarker) {
				m = Message{Role: "meta", Client: m.Client, Headless: true, Session: m.Session, Cwd: m.Cwd, Time: m.Time}
			}
			out = append(out, m)
		}
		if err != nil {
			break
		}
	}
	return out, pos, nil
}

// HostMarker is in every prompt x-mem sends to an agent to sort session
// candidates; a session that holds it is x-mem's own run, not a person.
const HostMarker = "〔x-mem 记忆整理任务，不是用户的对话〕"

// line covers the fields of all three formats.
type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	CreatedAt string `json:"created_at"`
	// Claude Code
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	IsCompact   bool            `json:"isCompactSummary"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Entrypoint  string          `json:"entrypoint"`
	Origin      json.RawMessage `json:"origin"`
	Message     json.RawMessage `json:"message"`
	// Codex
	Payload json.RawMessage `json:"payload"`
	// Antigravity
	Source  string `json:"source"`
	Content string `json:"content"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func texts(content json.RawMessage, kinds ...string) []string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []string{s}
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		for _, k := range kinds {
			if b.Type == k && b.Text != "" {
				out = append(out, b.Text)
			}
		}
	}
	return out
}

// userWords keeps what the person typed from the parts of a user message.
func userWords(parts []string) string {
	var keep []string
	for _, p := range parts {
		if noiseRe.MatchString(p) {
			continue
		}
		p = strings.TrimSpace(reminderRe.ReplaceAllString(p, ""))
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n")
}

func parse(raw []byte) (Message, bool) {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return Message{}, false
	}
	switch {
	case len(l.Payload) > 0: // Codex
		return parseCodex(l)
	case l.Type == "USER_INPUT" || l.Type == "PLANNER_RESPONSE": // Antigravity
		return parseAntigravity(l)
	}
	return parseClaude(l)
}

func parseClaude(l line) (Message, bool) {
	if strings.HasPrefix(l.Entrypoint, "sdk-") {
		return Message{Role: "meta", Client: Claude, Headless: true, Cwd: l.Cwd}, true
	}
	if l.IsMeta || l.IsSidechain || l.IsCompact || (l.Type != "user" && l.Type != "assistant") {
		return Message{}, false
	}
	if len(l.Origin) > 0 {
		var o struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(l.Origin, &o) == nil && o.Kind != "" && o.Kind != "human" {
			return Message{}, false
		}
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(l.Message, &m) != nil {
		return Message{}, false
	}
	var text string
	if l.Type == "user" {
		text = userWords(texts(m.Content, "text"))
	} else {
		text = strings.TrimSpace(strings.Join(texts(m.Content, "text"), "\n"))
	}
	if text == "" {
		return Message{}, false
	}
	return Message{Role: l.Type, Text: text, Client: Claude, Session: l.SessionID, Time: l.Timestamp, Cwd: l.Cwd}, true
}

func parseCodex(l line) (Message, bool) {
	var p struct {
		Type       string          `json:"type"`
		Role       string          `json:"role"`
		ID         string          `json:"id"`
		Cwd        string          `json:"cwd"`
		Originator string          `json:"originator"`
		Content    json.RawMessage `json:"content"`
	}
	if json.Unmarshal(l.Payload, &p) != nil {
		return Message{}, false
	}
	switch {
	case l.Type == "session_meta":
		return Message{Role: "meta", Client: Codex, Session: p.ID, Cwd: p.Cwd,
			Headless: strings.Contains(p.Originator, "exec")}, true
	case l.Type == "turn_context" && p.Cwd != "":
		return Message{Role: "meta", Client: Codex, Cwd: p.Cwd}, true
	case l.Type != "response_item" || p.Type != "message":
		return Message{}, false
	}
	var text string
	switch p.Role {
	case "user":
		text = userWords(texts(p.Content, "input_text"))
	case "assistant":
		text = strings.TrimSpace(strings.Join(texts(p.Content, "output_text"), "\n"))
	default:
		return Message{}, false
	}
	if text == "" {
		return Message{}, false
	}
	return Message{Role: p.Role, Text: text, Client: Codex, Time: l.Timestamp}, true
}

func parseAntigravity(l line) (Message, bool) {
	text := l.Content
	role := "assistant"
	if l.Type == "USER_INPUT" {
		if l.Source != "" && l.Source != "USER_EXPLICIT" {
			return Message{}, false
		}
		role = "user"
		if m := requestRe.FindStringSubmatch(text); m != nil {
			text = m[1]
		} else {
			text = agMetaRe.ReplaceAllString(text, "")
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, false
	}
	return Message{Role: role, Text: text, Client: Antigravity, Time: l.CreatedAt}, true
}

// Recent lists the session files of all three agents changed within the
// window, newest first.
func Recent(within time.Duration, max int) []string {
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	now := time.Now()
	cut := now.Add(-within)
	add := func(paths []string) {
		for _, p := range paths {
			if st, err := os.Stat(p); err == nil && st.ModTime().After(cut) {
				files = append(files, f{p, st.ModTime()})
			}
		}
	}
	m, _ := filepath.Glob(filepath.Join(Dir(), "*", "*.jsonl"))
	add(m)
	// Codex files sit in one folder per day: only look at the days in the window.
	for d := cut.AddDate(0, 0, -1); !d.After(now.AddDate(0, 0, 1)); d = d.AddDate(0, 0, 1) {
		m, _ := filepath.Glob(filepath.Join(CodexDir(), d.Format("2006/01/02"), "*.jsonl"))
		add(m)
	}
	for _, dir := range AntigravityDirs() {
		m, _ := filepath.Glob(filepath.Join(dir, "brain", "*", ".system_generated", "logs", "transcript.jsonl"))
		add(m)
	}
	m, _ = filepath.Glob(filepath.Join(HookTranscriptsDir(), "*.jsonl"))
	add(m)
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

// longestRun is the longest stretch of Chinese characters in s, used as a
// cheap pre-filter: whatever punctuation, spacing or letter case the quote
// was copied with, this part is in the file byte for byte.
func longestRun(s string) string {
	best, cur := "", []rune{}
	flush := func() {
		if len(cur) > len([]rune(best)) {
			best = string(cur)
		}
		cur = cur[:0]
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	if len([]rune(best)) < 3 {
		return ""
	}
	return best
}

// Found is where a quote was found.
type Found struct {
	Session string // "<client>:<session id>"
	Path    string
	Time    string
	Cwd     string
}

// FindQuote looks for a user message containing quote in the session files
// of all three agents changed within the window. Only what a person typed
// counts: a quote that appears in a tool result, a web page the agent read,
// or a headless run is not the user's.
func FindQuote(quote string, within time.Duration) (Found, bool) {
	q := Norm(quote)
	if len([]rune(q)) < 4 {
		return Found{}, false
	}
	key := longestRun(quote)
	for _, p := range Recent(within, 300) {
		// Session files can be large: skip the ones that cannot contain the
		// quote before parsing any JSON.
		if key != "" {
			data, err := os.ReadFile(p)
			if err != nil || !bytes.Contains(data, []byte(key)) {
				continue
			}
		}
		msgs, _, err := ReadFrom(p, 0)
		if err != nil {
			continue
		}
		headless, cwd, sid := false, "", ""
		for _, m := range msgs {
			if m.Role == "meta" {
				headless = headless || m.Headless
				if m.Cwd != "" {
					cwd = m.Cwd
				}
				if m.Session != "" {
					sid = m.Session
				}
			}
		}
		if headless {
			continue
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if m.Role == "user" && strings.Contains(Norm(m.Text), q) {
				id := m.Session
				if sid != "" && ClientOf(p) == Codex {
					id = sid
				}
				if id == "" {
					id = SessionOf(p)
				}
				if m.Cwd != "" {
					cwd = m.Cwd
				}
				return Found{Session: ClientOf(p) + ":" + id, Path: p, Time: m.Time, Cwd: cwd}, true
			}
		}
	}
	return Found{}, false
}
