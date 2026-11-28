package hook

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/transcript"
)

// hookTranscriptPath is where x-mem logs user prompts for agents whose own
// session files are compressed or binary (DSH, Cursor, Grok CLI), so quote
// verification and session capture work the same way as in Claude Code.
func hookTranscriptPath(l config.Layers, sessionID string) string {
	id := sessionID
	if id == "" {
		id = "no-session"
	}
	return filepath.Join(l.State, "transcripts", safeID.ReplaceAllString(id, "_")+".jsonl")
}

func recordUserTurn(path string, in Input, now time.Time) {
	if path == "" || strings.TrimSpace(in.Prompt) == "" || strings.Contains(in.Prompt, transcript.HostMarker) {
		return
	}
	rec := map[string]any{
		"type":      "user",
		"sessionId": in.SessionID,
		"cwd":       in.Cwd,
		"timestamp": now.Format(time.RFC3339),
		"message":   map[string]any{"content": in.Prompt},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	appendLine(path, string(b))
}

func prefixSession(prefix, id string) string {
	if id == "" || strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + id
}

// Cursor's hooks (~/.cursor/hooks.json) send conversation_id, prompt, command,
// cwd and workspace_roots.
type cursorInput struct {
	ConversationID string          `json:"conversation_id"`
	SessionID      string          `json:"session_id"`
	Prompt         string          `json:"prompt"`
	Command        string          `json:"command"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Cwd            string          `json:"cwd"`
	WorkspaceRoots []string        `json:"workspace_roots"`
}

func fromCursor(data []byte) Input {
	var c cursorInput
	_ = json.Unmarshal(data, &c)
	sid := c.ConversationID
	if sid == "" {
		sid = c.SessionID
	}
	cwd := c.Cwd
	if cwd == "" && len(c.WorkspaceRoots) > 0 {
		cwd = c.WorkspaceRoots[0]
	}
	in := Input{
		SessionID: prefixSession("cursor-", sid),
		Prompt:    c.Prompt,
		Cwd:       cwd,
		ToolName:  c.ToolName,
		ToolInput: c.ToolInput,
	}
	if in.ToolName == "" && c.Command != "" {
		b, _ := json.Marshal(map[string]any{"command": c.Command})
		in.ToolName, in.ToolInput = "Bash", b
	}
	return in
}

func toCursor(event, out string) string {
	switch event {
	case "prompt":
		return `{"continue":true}`
	case "stop":
		return "{}"
	}
	var o struct {
		H struct {
			AdditionalContext        string `json:"additionalContext"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if out == "" || json.Unmarshal([]byte(out), &o) != nil {
		if event == "guard" {
			return `{"permission":"allow"}`
		}
		return "{}"
	}
	switch {
	case event == "guard" && o.H.PermissionDecision == "deny":
		b, _ := json.Marshal(map[string]any{
			"permission":    "deny",
			"agent_message": o.H.PermissionDecisionReason,
			"user_message":  "x-mem 规则拦截",
		})
		return string(b)
	case event == "guard":
		return `{"permission":"allow"}`
	case o.H.AdditionalContext != "":
		b, _ := json.Marshal(map[string]any{"additional_context": o.H.AdditionalContext})
		return string(b)
	}
	return "{}"
}

// Grok CLI hooks (~/.grok/hooks/*.json) send camelCase fields (sessionId,
// workspaceRoot, toolName, toolInput) and accept {"decision":"allow"|"deny"}.
type grokInput struct {
	SessionID      string          `json:"sessionId"`
	SessionIDSnake string          `json:"session_id"`
	Prompt         string          `json:"prompt"`
	Cwd            string          `json:"cwd"`
	WorkspaceRoot  string          `json:"workspaceRoot"`
	ToolName       string          `json:"toolName"`
	ToolNameSnake  string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"toolInput"`
	ToolInputSnake json.RawMessage `json:"tool_input"`
}

func fromGrok(data []byte) Input {
	var g grokInput
	_ = json.Unmarshal(data, &g)
	sid := g.SessionID
	if sid == "" {
		sid = g.SessionIDSnake
	}
	cwd := g.Cwd
	if cwd == "" {
		cwd = g.WorkspaceRoot
	}
	tool := g.ToolName
	if tool == "" {
		tool = g.ToolNameSnake
	}
	tin := g.ToolInput
	if len(tin) == 0 {
		tin = g.ToolInputSnake
	}
	return Input{
		SessionID: prefixSession("grok-", sid),
		Prompt:    g.Prompt,
		Cwd:       cwd,
		ToolName:  tool,
		ToolInput: tin,
	}
}

func toGrok(event, out string) string {
	if event != "guard" {
		return out
	}
	var o struct {
		H struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if out != "" && json.Unmarshal([]byte(out), &o) == nil && o.H.PermissionDecision == "deny" {
		b, _ := json.Marshal(map[string]any{"decision": "deny", "reason": o.H.PermissionDecisionReason})
		return string(b)
	}
	return `{"decision":"allow"}`
}
