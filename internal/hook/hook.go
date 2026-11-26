// Package hook implements the Claude Code hooks. They read the hook's JSON on
// stdin and print a JSON reply on stdout, or nothing at all. They never fail
// the conversation: any error goes to state/hook.log and the hook stays quiet.
package hook

import (
	"crypto/sha1"
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

	"x-mem/internal/capture"
	"x-mem/internal/config"
	"x-mem/internal/guard"
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
	SessionID      string          `json:"session_id"`
	Prompt         string          `json:"prompt"`
	Cwd            string          `json:"cwd"`
	Event          string          `json:"hook_event_name"`
	TranscriptPath string          `json:"transcript_path"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Client         string          `json:"-"`
}

// GuardBudget is the most a PreToolUse reminder may add, in tokens.
const GuardBudget = 800

type output struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Run executes one Claude Code hook event. It returns what to print
// (possibly empty).
func Run(event string, stdin io.Reader, now time.Time) string {
	return RunAs(Claude, event, stdin, now)
}

// Clients whose hooks x-mem speaks.
const (
	Claude      = "claude"
	Codex       = "codex"
	Antigravity = "antigravity"
)

// RunAs executes one hook event for a client. Codex uses Claude Code's
// format; Antigravity's is translated on the way in and out.
func RunAs(client, event string, stdin io.Reader, now time.Time) string {
	if os.Getenv("X_MEM_DISABLE") != "" { // x-mem's own host runs
		return quiet(client)
	}
	data, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	var in Input
	if client == Antigravity {
		in = fromAntigravity(data)
	} else {
		_ = json.Unmarshal(data, &in)
	}
	in.Client = client
	if in.Client == Codex {
		if in.SessionID != "" {
			in.SessionID = "codex-" + in.SessionID
		}
		if in.ToolName == "apply_patch" {
			in.ToolName, in.ToolInput = patchTool(in.ToolInput)
		}
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	l, err := config.Resolve(cwd)
	if err != nil {
		return quiet(client)
	}
	out, err := run(event, in, l, now)
	if err != nil {
		logErr(l, client+" "+event, err)
		return quiet(client)
	}
	if client == Antigravity {
		return toAntigravity(out)
	}
	return out
}

// quiet is the reply that changes nothing.
func quiet(client string) string {
	if client == Antigravity {
		return "{}"
	}
	return ""
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
	case "guard":
		return guardHook(in, l)
	case "stop":
		return stop(in, l, now)
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
	var others []*index.Doc
	for _, d := range ix.Docs {
		if !d.Active || d.Layer == "kb" || inject.Suspicious(d) {
			continue
		}
		switch {
		case d.IsRule():
			rules = append(rules, d)
		case d.Layer == "project" && d.Type == "decision" && d.Updated >= cutoff:
			recent = append(recent, d)
		default:
			others = append(others, d)
		}
	}
	if len(rules) == 0 && len(recent) == 0 && len(others) == 0 {
		return "", nil
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Layer > rules[j].Layer })
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].Updated > recent[j].Updated })
	// The catalog: everything else, project first, then the most recently
	// changed, so what is left out when the budget runs short is the oldest.
	sort.SliceStable(others, func(i, j int) bool {
		if others[i].Layer != others[j].Layer {
			return others[i].Layer > others[j].Layer
		}
		return others[i].Updated > others[j].Updated
	})
	return reply("SessionStart", inject.StartBlock(rules, recent, others, StartBudget)), nil
}

func prompt(in Input, l config.Layers, now time.Time) (string, error) {
	st := loadSession(l, in.SessionID)
	if in.Client == Antigravity {
		// PreInvocation runs before every model call of a turn: handle each
		// message the user sends once.
		key := fmt.Sprintf("%x", sha1.Sum([]byte(in.Prompt)))
		if in.Prompt == "" || st.LastPrompt == key {
			return "", nil
		}
		st.LastPrompt = key
		_ = saveSession(l, in.SessionID, st)
	}
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
	res := recall.Search(ix, in.Prompt, recall.Options{Limit: 5, Now: now, NoRelated: true})
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

// guardHook runs before every tool call. Rules whose trigger matches are
// handed to the agent in full; a block rule refuses the call and says which
// rule. No match, no output.
func guardHook(in Input, l config.Layers) (string, error) {
	if in.ToolName == "" {
		return "", nil
	}
	ix, err := index.Open(l)
	if err != nil {
		return "", err
	}
	defer ix.Close()
	var block, advise []*index.Doc
	for _, d := range ix.Docs {
		if !d.Active || d.Layer == "kb" || len(d.Triggers) == 0 || inject.Suspicious(d) {
			continue
		}
		for _, t := range d.Triggers {
			if guard.Match(t, in.ToolName, in.ToolInput) {
				if d.Enforcement == "block" {
					block = append(block, d)
				} else {
					advise = append(advise, d)
				}
				break
			}
		}
	}
	if len(block) > 0 {
		text := inject.GuardBlock("这个动作被用户的规则拦下了：", append(block, advise...), GuardBudget)
		return guardReply("deny", text), nil
	}
	if len(advise) > 0 {
		if in.Client != Antigravity {
			return guardReply("", inject.GuardBlock("执行这个动作前，用户有规则要你遵守：", advise, GuardBudget)), nil
		}
		// Antigravity cannot add context before a tool call. Stop the call
		// once per rule and conversation with the rule as the reason; the
		// agent reads it and runs the call again, which then goes through.
		st := loadSession(l, in.SessionID)
		var fresh []*index.Doc
		for _, d := range advise {
			if !st.Advised[d.ID] {
				fresh = append(fresh, d)
				st.Advised[d.ID] = true
			}
		}
		if len(fresh) == 0 {
			return "", nil
		}
		_ = saveSession(l, in.SessionID, st)
		text := inject.GuardBlock("执行这个动作前，用户有规则要你遵守（这是提醒，不是禁止：照规则做完后重新执行同一个动作即可）：", fresh, GuardBudget)
		return guardReply("deny", text), nil
	}
	return "", nil
}

func guardReply(decision, text string) string {
	out := map[string]any{"hookEventName": "PreToolUse"}
	if decision == "deny" {
		out["permissionDecision"] = "deny"
		out["permissionDecisionReason"] = text
	} else {
		out["additionalContext"] = text
	}
	data, _ := json.Marshal(map[string]any{"hookSpecificOutput": out})
	return string(data)
}

// stop runs at the end of every turn. It only records that the session has
// new lines and wakes the writer in the background; it reads nothing, so it
// costs the same however long the session is.
func stop(in Input, l config.Layers, now time.Time) (string, error) {
	if !capture.LoadState(l).Enabled || in.TranscriptPath == "" {
		return "", nil
	}
	if err := capture.AddSignal(l, capture.Signal{Path: in.TranscriptPath, Session: in.SessionID, Cwd: in.Cwd, At: now}); err != nil {
		return "", err
	}
	if os.Getenv("X_MEM_NO_SPAWN") == "" {
		if exe, err := os.Executable(); err == nil {
			spawnDetached(exe, "writer", "--once")
		}
	}
	return "", nil
}

func reply(event, ctx string) string {
	var o output
	o.HookSpecificOutput.HookEventName = event
	o.HookSpecificOutput.AdditionalContext = ctx
	data, _ := json.Marshal(o)
	return string(data)
}

type session struct {
	OptOut     bool            `json:"opt_out"`
	Injected   map[string]bool `json:"injected"`
	Advised    map[string]bool `json:"advised,omitempty"`     // Antigravity: rules already shown before a tool call
	LastPrompt string          `json:"last_prompt,omitempty"` // Antigravity: the input already handled
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
	if st.Advised == nil {
		st.Advised = map[string]bool{}
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
