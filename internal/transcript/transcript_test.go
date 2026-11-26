package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (claude, codex, ag string) {
	root := t.TempDir()
	claude, codex, ag = filepath.Join(root, "claude"), filepath.Join(root, "codex"), filepath.Join(root, "antigravity")
	t.Setenv("X_MEM_TRANSCRIPTS", claude)
	t.Setenv("X_MEM_CODEX_SESSIONS", codex)
	t.Setenv("X_MEM_ANTIGRAVITY_DIRS", ag)
	return
}

func TestThreeFormats(t *testing.T) {
	claude, codex, ag := setup(t)
	day := time.Now().Format("2006/01/02")
	cx := filepath.Join(codex, day, "rollout-2026-11-26T10-00-00-019a.jsonl")
	write(t, cx,
		`{"type":"session_meta","timestamp":"2026-11-26T10:00:00Z","payload":{"id":"019a-codex","cwd":"/work/api","originator":"codex_cli_rs"}}`,
		`{"type":"response_item","timestamp":"2026-11-26T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"},{"type":"input_text","text":"# AGENTS.md instructions for /work/api"},{"type":"input_text","text":"以后缓存过期时间统一改成十分钟"}]}}`,
		`{"type":"response_item","timestamp":"2026-11-26T10:00:02Z","payload":{"type":"function_call","name":"shell"}}`,
		`{"type":"response_item","timestamp":"2026-11-26T10:00:03Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好的，已改成 10 分钟。"}]}}`)
	msgs, _, _ := ReadFrom(cx, 0)
	if len(msgs) != 3 || msgs[0].Role != "meta" || msgs[0].Cwd != "/work/api" || msgs[1].Text != "以后缓存过期时间统一改成十分钟" || msgs[2].Role != "assistant" {
		t.Fatalf("codex: %+v", msgs)
	}
	agp := filepath.Join(ag, "brain", "conv-42", ".system_generated", "logs", "transcript.jsonl")
	write(t, agp,
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-26T10:05:00Z","content":"<USER_REQUEST>\n发版统一放在每周二上午\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-11-26T18:05:00+08:00.\n</ADDITIONAL_METADATA>"}`,
		`{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE","created_at":"2026-11-26T10:05:00Z"}`,
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-26T10:05:01Z","content":"收到，我记一下。","tool_calls":[]}`,
		`{"step_index":3,"source":"MODEL","type":"RUN_COMMAND","status":"DONE","created_at":"2026-11-26T10:05:02Z","content":"Created At: x\nOutput: 发版统一放在每周五"}`)
	msgs, _, _ = ReadFrom(agp, 0)
	if len(msgs) != 2 || msgs[0].Text != "发版统一放在每周二上午" || msgs[0].Session != "conv-42" || msgs[1].Role != "assistant" {
		t.Fatalf("antigravity: %+v", msgs)
	}
	cl := filepath.Join(claude, "-work-web", "s1.jsonl")
	write(t, cl,
		`{"type":"user","sessionId":"s1","cwd":"/work/web","message":{"role":"user","content":"前端统一用 pnpm，别再用 npm"}}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"子代理里说的话"}}`)
	head := filepath.Join(claude, "-x-mem-host", "h1.jsonl")
	write(t, head, `{"type":"user","entrypoint":"sdk-cli","sessionId":"h1","message":{"role":"user","content":"前端统一用 yarn 这是宿主任务里的话"}}`)

	for quote, want := range map[string]string{
		"以后缓存过期时间统一改成十分钟":    "codex:019a-codex",
		"发版统一放在每周二上午":        "antigravity:conv-42",
		"前端统一用 PNPM，别再用 npm": "claude:s1",
	} {
		f, ok := FindQuote(quote, time.Hour)
		if !ok || f.Session != want {
			t.Errorf("%s: got %+v %v, want %s", quote, f, ok, want)
		}
	}
	for _, quote := range []string{"发版统一放在每周五", "子代理里说的话", "前端统一用 yarn 这是宿主任务里的话"} {
		if f, ok := FindQuote(quote, time.Hour); ok {
			t.Errorf("%q is not the user's words, found in %s", quote, f.Path)
		}
	}
}
