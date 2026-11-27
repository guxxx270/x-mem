package hook

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Antigravity's hooks (~/.gemini/config/hooks.json) send camelCase JSON:
// conversationId, lastUserInput, transcriptPath, workspacePaths, and for tool
// events toolCall{name, args}. It has no prompt-submit event; x-mem's prompt
// hook runs on PreInvocation and handles each user message once.
type agInput struct {
	ConversationID string          `json:"conversationId"`
	LastUserInput  string          `json:"lastUserInput"`
	TranscriptPath string          `json:"transcriptPath"`
	WorkspacePaths []string        `json:"workspacePaths"`
	InvocationNum  int             `json:"invocationNum"`
	ToolCall       *agToolCall     `json:"toolCall"`
	Raw            json.RawMessage `json:"-"`
}

type agToolCall struct {
	Name string                     `json:"name"`
	Args map[string]json.RawMessage `json:"args"`
}

var userRequestRe = regexp.MustCompile(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)

func fromAntigravity(data []byte) Input {
	var a agInput
	_ = json.Unmarshal(data, &a)
	in := Input{
		SessionID:      "ag-" + a.ConversationID,
		Prompt:         a.LastUserInput,
		TranscriptPath: a.TranscriptPath,
	}
	if m := userRequestRe.FindStringSubmatch(in.Prompt); m != nil {
		in.Prompt = strings.TrimSpace(m[1])
	}
	if len(a.WorkspacePaths) > 0 {
		in.Cwd = a.WorkspacePaths[0]
	}
	if a.ToolCall != nil {
		in.ToolName, in.ToolInput = canonicalTool(a.ToolCall.Name, a.ToolCall.Args)
	}
	return in
}

// argString reads an Antigravity tool argument. Some arrive JSON-encoded a
// second time ("\"/path\""), so a quoted string is unquoted once more.
func argString(args map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := args[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		var inner string
		if strings.HasPrefix(s, `"`) && json.Unmarshal([]byte(s), &inner) == nil {
			s = inner
		}
		return s
	}
	return ""
}

// canonicalTool maps Antigravity's tools onto the names rule triggers use,
// so one rule ("Bash(git push*)") works in every agent.
func canonicalTool(name string, args map[string]json.RawMessage) (string, json.RawMessage) {
	enc := func(tool string, v map[string]any) (string, json.RawMessage) {
		b, _ := json.Marshal(v)
		return tool, b
	}
	switch name {
	case "run_command":
		return enc("Bash", map[string]any{"command": argString(args, "CommandLine", "Command", "command")})
	case "write_to_file":
		return enc("Write", map[string]any{"file_path": argString(args, "TargetFile", "AbsolutePath", "file_path")})
	case "replace_file_content", "multi_replace_file_content", "edit_file":
		return enc("Edit", map[string]any{"file_path": argString(args, "TargetFile", "AbsolutePath", "file_path")})
	case "view_file", "view_file_outline":
		return enc("Read", map[string]any{"file_path": argString(args, "AbsolutePath", "TargetFile", "file_path")})
	case "read_url_content":
		return enc("WebFetch", map[string]any{"url": argString(args, "Url", "url")})
	case "search_web":
		return enc("WebSearch", map[string]any{"query": argString(args, "query", "Query")})
	}
	plain := map[string]any{}
	for k := range args {
		plain[k] = argString(args, k)
	}
	b, _ := json.Marshal(plain)
	return name, b
}

// agAllow lets an Antigravity tool call through to the next hook or the
// default policy. PreToolUse must always say so: "decision" is required, and
// {} is read as a refusal.
const agAllow = `{"decision":"allow"}`

// toAntigravity turns x-mem's Claude-format reply into Antigravity's:
// context goes in injectSteps, a refusal is decision "deny" with a reason,
// and a tool call x-mem has nothing against is "allow".
func toAntigravity(event, out string) string {
	quietReply := "{}"
	if event == "guard" {
		quietReply = agAllow
	}
	var o struct {
		H struct {
			AdditionalContext        string `json:"additionalContext"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if out == "" || json.Unmarshal([]byte(out), &o) != nil {
		return quietReply
	}
	var reply any
	switch {
	case o.H.PermissionDecision == "deny":
		reply = map[string]any{"decision": "deny", "reason": o.H.PermissionDecisionReason}
	case event == "guard":
		// PreToolUse cannot add context; anything short of a refusal is allow.
		return agAllow
	case o.H.AdditionalContext != "":
		reply = map[string]any{"injectSteps": []any{
			map[string]any{"systemMessage": map[string]any{"systemMessage": o.H.AdditionalContext}},
		}}
	default:
		return quietReply
	}
	b, _ := json.Marshal(reply)
	return string(b)
}

var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

// patchTool maps Codex's apply_patch onto Edit, with every file the patch
// touches, so "Edit(*.sql)" applies to it.
func patchTool(input json.RawMessage) (string, json.RawMessage) {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	var files []string
	for _, m := range patchFileRe.FindAllStringSubmatch(in.Command, -1) {
		files = append(files, strings.TrimSpace(m[1]))
	}
	b, _ := json.Marshal(map[string]any{"file_paths": files})
	return "Edit", b
}
