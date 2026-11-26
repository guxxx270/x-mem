package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/capture"
	"x-mem/internal/config"
)

func toolCall(t *testing.T, tool string, input map[string]any, cwd string) map[string]any {
	t.Helper()
	ti, _ := json.Marshal(input)
	in, _ := json.Marshal(Input{SessionID: "g", Cwd: cwd, ToolName: tool, ToolInput: ti})
	out := Run("guard", strings.NewReader(string(in)), time.Now())
	if out == "" {
		return nil
	}
	var o struct {
		H map[string]any `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("bad guard output %q", out)
	}
	return o.H
}

func TestGuardRemindsBeforePushOnly(t *testing.T) {
	_, proj := setup(t)
	got := toolCall(t, "Bash", map[string]any{"command": "make build && git push origin main"}, proj)
	if got == nil || !strings.Contains(fmt.Sprint(got["additionalContext"]), "推送到 main 之前先跑全量测试") {
		t.Fatalf("push should bring the rule: %v", got)
	}
	if got["permissionDecision"] != nil {
		t.Fatalf("an advise rule must not decide permission: %v", got)
	}
	for _, cmd := range []string{"ls -la", "git status", "echo git push"} {
		if got := toolCall(t, "Bash", map[string]any{"command": cmd}, proj); got != nil {
			t.Fatalf("%q should stay silent, got %v", cmd, got)
		}
	}
	if got := toolCall(t, "Read", map[string]any{"file_path": "/tmp/x"}, proj); got != nil {
		t.Fatalf("other tools stay silent")
	}
}

func TestGuardBlocks(t *testing.T) {
	home, proj := setup(t)
	os.WriteFile(filepath.Join(home, "personal/rules/nodrop.md"), []byte("---\nid: nodrop\ntitle: 不许删库\ntype: rule\ntriggers: [\"Bash(*drop database*)\", \"Bash(*DROP DATABASE*)\"]\nenforcement: block\n---\n任何环境都不许 drop database，要删找 DBA。\n"), 0o644)
	got := toolCall(t, "Bash", map[string]any{"command": "psql -c 'DROP DATABASE orders'"}, proj)
	if got == nil || got["permissionDecision"] != "deny" || !strings.Contains(fmt.Sprint(got["permissionDecisionReason"]), "找 DBA") {
		t.Fatalf("want deny with the rule, got %v", got)
	}
}

func TestGuardIgnoresSupersededRules(t *testing.T) {
	home, proj := setup(t)
	os.WriteFile(filepath.Join(home, "personal/rules/push2.md"), []byte("---\nid: push2\ntitle: 推送前跑快速测试\ntype: rule\ntriggers: [\"Bash(git push*)\"]\nrelations:\n  - op: \"@supersedes\"\n    target: push\n---\n推送前跑 make test-quick 就够了。\n"), 0o644)
	got := toolCall(t, "Bash", map[string]any{"command": "git push"}, proj)
	ctx := fmt.Sprint(got["additionalContext"])
	if !strings.Contains(ctx, "test-quick") || strings.Contains(ctx, "全量测试") {
		t.Fatalf("only the current rule should come: %s", ctx)
	}
}

func TestStopCostDoesNotGrowWithSession(t *testing.T) {
	home, proj := setup(t)
	t.Setenv("X_MEM_NO_SPAWN", "1")
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	capture.SaveState(l, capture.State{Enabled: true, Since: time.Now()})
	tr := filepath.Join(t.TempDir(), "long.jsonl")
	f, _ := os.Create(tr)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(f, `{"type":"user","sessionId":"long","message":{"role":"user","content":"第 %d 轮"}}`+"\n", i)
		fmt.Fprintf(f, `{"type":"assistant","sessionId":"long","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}`+"\n", strings.Repeat("长回复", 200))
	}
	f.Close()
	stopOnce := func(path string) time.Duration {
		in, _ := json.Marshal(Input{SessionID: "long", Cwd: proj, TranscriptPath: path})
		start := time.Now()
		Run("stop", strings.NewReader(string(in)), time.Now())
		return time.Since(start)
	}
	short := filepath.Join(t.TempDir(), "short.jsonl")
	os.WriteFile(short, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	var a, b time.Duration
	for i := 0; i < 20; i++ {
		a += stopOnce(short)
		b += stopOnce(tr)
	}
	if b > 3*a+20*time.Millisecond {
		t.Fatalf("stop on a 500-turn session took %v vs %v on a short one", b/20, a/20)
	}
	data, _ := os.ReadFile(filepath.Join(l.State, "capture-signals.jsonl"))
	if strings.Count(string(data), "\n") != 40 {
		t.Fatalf("want 40 signals, got:\n%s", data)
	}
}

func TestDisabledForHostRuns(t *testing.T) {
	_, proj := setup(t)
	t.Setenv("X_MEM_DISABLE", "1")
	if out := runHook(t, "start", "s", "", proj); out != "" {
		t.Fatal("hooks must stay silent inside x-mem's own host runs")
	}
}
