// Package hook implements the Claude Code hooks. They read the hook's JSON on
// stdin and print a JSON reply on stdout, or nothing at all. They never fail
// the conversation: any error goes to state/hook.log and the hook stays quiet.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/inject"
	"x-mem/internal/recall"
	"x-mem/internal/textutil"
)

// Budgets in model tokens.
const (
	StartBudget  = 1500
	PromptBudget = 600
	MaxPerPrompt = 2
	// Messages longer than this are mostly pasted code or logs.
	MaxPromptRunes = 3000
)

// OptOutMark in a message keeps the whole session out of memory.
const OptOutMark = "#不记"

// Input is the part of Claude Code's hook payload x-mem reads.
type Input struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
}

type output struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Run executes one hook event. It returns what to print (possibly empty).
func Run(event string, stdin io.Reader, now time.Time) string {
	var in Input
	data, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	_ = json.Unmarshal(data, &in)
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	l, err := config.Resolve(cwd)
	if err != nil {
		return ""
	}
	out, err := run(event, in, l, now)
	if err != nil {
		logErr(l, event, err)
		return ""
	}
	return out
}

func run(event string, in Input, l config.Layers, now time.Time) (string, error) {
	if len(l.Roots()) == 0 {
		return "", nil
	}
	switch event {
	case "start":
		return start(in, l, now)
	case "prompt":
		return prompt(in, l, now)
	}
	return "", fmt.Errorf("unknown hook event %q", event)
}

func start(in Input, l config.Layers, now time.Time) (string, error) {
	ix, err := index.Open(l)
	if err != nil {
		return "", err
	}
	var rules, recent []*index.Doc
	cutoff := now.AddDate(0, 0, -30).Format("2006-01-02")
	for _, d := range ix.Docs {
		if !d.Active || inject.Suspicious(d) {
			continue
		}
		switch {
		case d.IsRule():
			rules = append(rules, d)
		case d.Layer == "project" && d.Type == "decision" && d.Updated >= cutoff:
			recent = append(recent, d)
		}
	}
	if len(rules) == 0 && len(recent) == 0 {
		return "", nil
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Layer > rules[j].Layer })
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].Updated > recent[j].Updated })
	return reply("SessionStart", inject.StartBlock(rules, recent, StartBudget)), nil
}

func prompt(in Input, l config.Layers, now time.Time) (string, error) {
	st := loadSession(l, in.SessionID)
	if strings.Contains(in.Prompt, OptOutMark) {
		st.OptOut = true
		return "", saveSession(l, in.SessionID, st)
	}
	if st.OptOut || strings.TrimSpace(in.Prompt) == "" || utf8.RuneCountInString(in.Prompt) > MaxPromptRunes || looksLikePaste(in.Prompt) {
		return "", nil
	}
	ix, err := index.Open(l)
	if err != nil {
		return "", err
	}
	res := recall.Search(ix, in.Prompt, recall.Options{Limit: 5, Now: now})
	if res.Tier != recall.Direct {
		return "", nil
	}
	var picked []recall.Hit
	for _, h := range res.Hits {
		if h.Tier != recall.Direct || len(picked) >= MaxPerPrompt {
			break
		}
		if st.Injected[h.Doc.ID] || inject.Suspicious(h.Doc) {
			continue
		}
		picked = append(picked, h)
	}
	if len(picked) == 0 {
		return "", nil
	}
	block := inject.PromptBlock(picked)
	for len(picked) > 1 && estimate(block) > PromptBudget {
		picked = picked[:len(picked)-1]
		block = inject.PromptBlock(picked)
	}
	var ids []string
	for _, h := range picked {
		st.Injected[h.Doc.ID] = true
		ids = append(ids, h.Doc.ID)
	}
	_ = saveSession(l, in.SessionID, st)
	logRecall(l, in.SessionID, ids, now)
	return reply("UserPromptSubmit", block), nil
}

var codeLine = regexp.MustCompile(`^\s*([{}()\[\];]|//|#include|import |func |def |class |at |\d{4}-\d{2}-\d{2}[ T]\d|[A-Za-z_][\w.]*\(.*\)\s*[{;]?$)`)

// looksLikePaste is true for a message that is mostly code or a log.
func looksLikePaste(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) < 8 {
		return false
	}
	code := 0
	for _, l := range lines {
		if codeLine.MatchString(l) || strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") {
			code++
		}
	}
	return code*2 > len(lines)
}

func estimate(s string) int { return textutil.EstimateTokens(s) }

func reply(event, ctx string) string {
	var o output
	o.HookSpecificOutput.HookEventName = event
	o.HookSpecificOutput.AdditionalContext = ctx
	data, _ := json.Marshal(o)
	return string(data)
}

type session struct {
	OptOut   bool            `json:"opt_out"`
	Injected map[string]bool `json:"injected"`
}

var safeID = regexp.MustCompile(`[^A-Za-z0-9_\-]`)

func sessionPath(l config.Layers, id string) string {
	if id == "" {
		id = "no-session"
	}
	return filepath.Join(l.State, "sessions", safeID.ReplaceAllString(id, "_")+".json")
}

func loadSession(l config.Layers, id string) session {
	st := session{Injected: map[string]bool{}}
	if data, err := os.ReadFile(sessionPath(l, id)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Injected == nil {
		st.Injected = map[string]bool{}
	}
	return st
}

func saveSession(l config.Layers, id string, st session) error {
	p := sessionPath(l, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(st)
	return os.WriteFile(p, data, 0o644)
}

// IsOptedOut reports whether a session was marked #不记.
func IsOptedOut(l config.Layers, id string) bool { return loadSession(l, id).OptOut }

func logRecall(l config.Layers, sessionID string, ids []string, now time.Time) {
	rec, _ := json.Marshal(map[string]any{"t": now.Format(time.RFC3339), "event": "prompt", "session": sessionID, "ids": ids})
	appendLine(filepath.Join(l.State, "recalled.jsonl"), string(rec))
}

func logErr(l config.Layers, event string, err error) {
	appendLine(filepath.Join(l.State, "hook.log"), time.Now().Format(time.RFC3339)+" "+event+": "+err.Error())
}

func appendLine(path, line string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
