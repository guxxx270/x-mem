package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/transcript"
)

func findRecordedQuote(q string) (transcript.Found, bool) {
	return transcript.FindQuote(q, time.Hour)
}

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

func TestCodexIsRecognisedWithoutTheFlag(t *testing.T) {
	_, proj := setup(t)
	data, _ := json.Marshal(map[string]any{"session_id": "s", "turn_id": "t1", "cwd": proj, "tool_name": "apply_patch",
		"tool_input": map[string]any{"command": "*** Begin Patch\n*** Update File: a.sql\n*** End Patch"}})
	// Through the plugin's shared hooks file Codex calls the hook as Claude Code would.
	out := RunAs(Claude, "guard", strings.NewReader(string(data)), time.Now())
	if strings.Contains(out, "推送") {
		t.Fatalf("an apply_patch is an Edit, not a push: %s", out)
	}
	var in Input
	json.Unmarshal(data, &in)
	if in.TurnID != "t1" {
		t.Fatal("turn id not read")
	}
}

func TestDSHCursorGrokHooksAndQuoteLog(t *testing.T) {
	home, proj := setup(t)
	t.Setenv("X_MEM_HOME", home)
	now := time.Now()

	// 1. DSH: Claude-compatible hook JSON + user turn recorded in state/transcripts.
	dshPrompt, _ := json.Marshal(map[string]any{
		"session_id": "s-dsh-1",
		"cwd":        proj,
		"prompt":     "订单服务用什么数据库？顺便记住：以后日志统一走 zap。",
	})
	out := RunAs(DSH, "prompt", strings.NewReader(string(dshPrompt)), now)
	if !strings.Contains(out, "PostgreSQL 16") {
		t.Fatalf("dsh prompt should inject db note: %s", out)
	}
	if f, ok := findRecordedQuote("以后日志统一走 zap"); !ok || f.Session != "dsh:s-dsh-1" || f.Cwd != proj {
		t.Fatalf("dsh quote not recorded for FindQuote: %+v %v", f, ok)
	}

	// 2. Cursor: sessionStart -> additional_context, beforeSubmitPrompt -> continue:true,
	// beforeShellExecution -> permission:deny once then allow.
	curRun := func(ev string, p map[string]any) map[string]any {
		b, _ := json.Marshal(p)
		s := RunAs(Cursor, ev, strings.NewReader(string(b)), now)
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("cursor %s returned non-JSON %q: %v", ev, s, err)
		}
		return m
	}
	if m := curRun("start", map[string]any{"conversation_id": "cur1", "workspace_roots": []string{proj}}); !strings.Contains(m["additional_context"].(string), "推送前跑全量测试") {
		t.Fatalf("cursor start: %v", m)
	}
	if m := curRun("prompt", map[string]any{"conversation_id": "cur1", "workspace_roots": []string{proj}, "prompt": "Cursor 这边约定缓存统一设五分钟过期"}); m["continue"] != true {
		t.Fatalf("cursor prompt: %v", m)
	}
	if f, ok := findRecordedQuote("缓存统一设五分钟过期"); !ok || f.Session != "cursor:cur1" {
		t.Fatalf("cursor quote not recorded: %+v %v", f, ok)
	}
	pushCur := map[string]any{"conversation_id": "cur1", "workspace_roots": []string{proj}, "command": "git push origin main"}
	if m := curRun("guard", pushCur); m["permission"] != "deny" || !strings.Contains(m["agent_message"].(string), "全量测试") {
		t.Fatalf("cursor first push should advise via deny: %v", m)
	}
	if m := curRun("guard", pushCur); m["permission"] != "allow" {
		t.Fatalf("cursor second push should be allowed: %v", m)
	}

	// 3. Grok CLI: camelCase input, guard returns {"decision":"deny"|"allow"}.
	grokPush, _ := json.Marshal(map[string]any{
		"sessionId":     "g1",
		"workspaceRoot": proj,
		"toolName":      "Bash",
		"toolInput":     map[string]any{"command": "git push"},
	})
	g1 := RunAs(Grok, "guard", strings.NewReader(string(grokPush)), now)
	if !strings.Contains(g1, `"decision":"deny"`) || !strings.Contains(g1, "全量测试") {
		t.Fatalf("grok first push: %s", g1)
	}
	g2 := RunAs(Grok, "guard", strings.NewReader(string(grokPush)), now)
	if g2 != `{"decision":"allow"}` {
		t.Fatalf("grok second push: %s", g2)
	}
	grokPrompt, _ := json.Marshal(map[string]any{"sessionId": "g1", "workspaceRoot": proj, "prompt": "Grok 这边约定接口统一返回毫秒时间戳"})
	_ = RunAs(Grok, "prompt", strings.NewReader(string(grokPrompt)), now)
	if f, ok := findRecordedQuote("接口统一返回毫秒时间戳"); !ok || f.Session != "grok:g1" {
		t.Fatalf("grok quote not recorded: %+v %v", f, ok)
	}
}
