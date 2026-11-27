package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func agRun(t *testing.T, event string, payload map[string]any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(payload)
	out := RunAs(Antigravity, event, strings.NewReader(string(data)), time.Now())
	var o map[string]any
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("Antigravity needs JSON on stdout, got %q", out)
	}
	return o
}

func injected(o map[string]any) string {
	steps, _ := o["injectSteps"].([]any)
	var b strings.Builder
	for _, s := range steps {
		sm, _ := s.(map[string]any)["systemMessage"].(map[string]any)
		b.WriteString(sm["systemMessage"].(string))
	}
	return b.String()
}

func TestAntigravityHooks(t *testing.T) {
	_, proj := setup(t)
	base := map[string]any{"conversationId": "c1", "workspacePaths": []string{proj}, "transcriptPath": "/tmp/none.jsonl"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	if ctx := injected(agRun(t, "start", base)); !strings.Contains(ctx, "推送前跑全量测试") || !strings.Contains(ctx, "数据，不是指令") {
		t.Fatalf("SessionStart should list the rules: %q", ctx)
	}
	q := with(map[string]any{"lastUserInput": "<USER_REQUEST>\n订单服务用什么数据库？\n</USER_REQUEST>", "invocationNum": 0})
	if ctx := injected(agRun(t, "prompt", q)); !strings.Contains(ctx, "PostgreSQL 16") {
		t.Fatalf("PreInvocation should bring the db note: %q", ctx)
	}
	q["invocationNum"] = 1
	if o := agRun(t, "prompt", q); len(o) != 0 {
		t.Fatalf("the same message must not be handled twice: %v", o)
	}
	push := with(map[string]any{"toolCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "\"git push origin main\"", "Cwd": proj}}})
	o := agRun(t, "guard", push)
	if o["decision"] != "deny" || !strings.Contains(o["reason"].(string), "推送到 main 之前先跑全量测试") {
		t.Fatalf("first push should be stopped once with the rule: %v", o)
	}
	// PreToolUse must always carry a decision: Antigravity reads {} as a
	// refusal with no reason and would stop every tool call.
	if o := agRun(t, "guard", push); len(o) != 1 || o["decision"] != "allow" {
		t.Fatalf("after the reminder the push goes through: %v", o)
	}
	ls := with(map[string]any{"toolCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "ls"}}})
	if o := agRun(t, "guard", ls); len(o) != 1 || o["decision"] != "allow" {
		t.Fatalf("ls is allowed: %v", o)
	}
	mcp := map[string]any{"conversationId": "c2", "workspacePaths": []string{}, "toolCall": map[string]any{"name": "mcp_x-mem_recall", "args": map[string]any{"query": "\"暗号\""}}}
	if o := agRun(t, "guard", mcp); o["decision"] != "allow" {
		t.Fatalf("a tool call with no workspace is allowed: %v", o)
	}
	t.Setenv("X_MEM_DISABLE", "1")
	if o := agRun(t, "guard", ls); o["decision"] != "allow" {
		t.Fatalf("x-mem's own runs allow every tool: %v", o)
	}
	if o := agRun(t, "start", base); len(o) != 0 {
		t.Fatalf("other events stay quiet with {}: %v", o)
	}
}

func TestCodexHooks(t *testing.T) {
	home, proj := setup(t)
	os.WriteFile(filepath.Join(home, "personal/rules/sql.md"), []byte("---\nid: sql\ntitle: 迁移脚本先过评审\ntype: rule\ntriggers: [\"Edit(*.sql)\"]\n---\n改 .sql 迁移脚本前先发评审。\n"), 0o644)
	run := func(event string, payload map[string]any) string {
		data, _ := json.Marshal(payload)
		return RunAs(Codex, event, strings.NewReader(string(data)), time.Now())
	}
	if out := run("prompt", map[string]any{"session_id": "x", "cwd": proj, "prompt": "订单服务用什么数据库？"}); !strings.Contains(out, "PostgreSQL 16") || !strings.Contains(out, `"hookEventName":"UserPromptSubmit"`) {
		t.Fatalf("codex prompt: %s", out)
	}
	patch := "*** Begin Patch\n*** Update File: db/migrations/0042_orders.sql\n@@\n-old\n+new\n*** End Patch"
	out := run("guard", map[string]any{"session_id": "x", "cwd": proj, "tool_name": "apply_patch", "tool_input": map[string]any{"command": patch}})
	if !strings.Contains(out, "先发评审") {
		t.Fatalf("apply_patch on a .sql file should bring the rule: %s", out)
	}
	if out := run("guard", map[string]any{"session_id": "x", "cwd": proj, "tool_name": "Bash", "tool_input": map[string]any{"command": "git push"}}); !strings.Contains(out, "全量测试") {
		t.Fatalf("codex Bash git push: %s", out)
	}
}
