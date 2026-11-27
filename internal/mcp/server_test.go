package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeRecallReadStatus(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "personal", "notes", "db.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("---\nid: db\ntitle: 订单服务数据库\n---\n订单服务的数据库用 PostgreSQL 16。\n"), 0o644)
	t.Setenv("X_MEM_HOME", home)
	t.Setenv("X_MEM_PROJECT", filepath.Join(home, "no-project"))

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall","arguments":{"query":"订单服务用什么数据库"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"id":"db"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"recall","arguments":{"query":"楼里的咖啡机又坏了"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
	}, "\n")
	var out bytes.Buffer
	if err := Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("want 6 replies (no reply to the notification), got %d:\n%s", len(lines), out.String())
	}
	text := func(i int) string {
		var r struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		json.Unmarshal([]byte(lines[i]), &r)
		if len(r.Result.Content) == 0 {
			return ""
		}
		return r.Result.Content[0].Text
	}
	for _, tool := range []string{`"recall"`, `"read"`, `"propose"`, `"status"`} {
		if !strings.Contains(lines[1], tool) {
			t.Fatalf("tools/list misses %s: %s", tool, lines[1])
		}
	}
	if got := text(2); !strings.HasPrefix(got, "memory: direct") || !strings.Contains(got, "PostgreSQL 16") {
		t.Fatalf("recall: %s", got)
	}
	if got := text(3); !strings.Contains(got, "状态：生效中") {
		t.Fatalf("read: %s", got)
	}
	if got := text(4); !strings.HasPrefix(got, "memory: none") {
		t.Fatalf("unrelated recall: %s", got)
	}
	if !strings.Contains(lines[5], "-32601") {
		t.Fatalf("unknown method must be an error: %s", lines[5])
	}
}

func TestInboxTool(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "personal", "notes"), 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		exec.Command("git", append([]string{"-C", filepath.Join(home, "personal")}, args...)...).Run()
	}
	t.Setenv("X_MEM_HOME", home)
	t.Setenv("X_MEM_PROJECT", filepath.Join(home, "no-project"))
	t.Setenv("HOME", home) // no session files: every quote is unverified

	call := func(name, args string) string {
		in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
		var out bytes.Buffer
		Serve(strings.NewReader(in), &out)
		var r struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		json.Unmarshal(out.Bytes(), &r)
		return r.Result.Content[0].Text
	}
	got := call("propose", `{"text":"周报统一用表格写","quote":"以后周报统一用表格写","kind":"preference"}`)
	if !strings.Contains(got, "待用户确认") {
		t.Fatalf("a quote not found here waits in the inbox: %s", got)
	}
	if got := call("inbox", `{"action":"list"}`); !strings.Contains(got, "周报统一用表格写") || !strings.Contains(got, "原话没在本机") {
		t.Fatalf("list: %s", got)
	}
	if got := call("inbox", `{"action":"approve","ids":["all"]}`); !strings.Contains(got, "写入 1 条") {
		t.Fatalf("approve all: %s", got)
	}
	if got := call("recall", `{"query":"周报用什么格式"}`); !strings.Contains(got, "表格") {
		t.Fatalf("approved note is recalled: %s", got)
	}
	if got := call("inbox", `{"action":"list"}`); got != "收件箱是空的。" {
		t.Fatalf("inbox should be empty: %s", got)
	}
}

func TestInitializeSendsInstructions(t *testing.T) {
	var out bytes.Buffer
	Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`), &out)
	if !strings.Contains(out.String(), "recall") || !strings.Contains(out.String(), "Claude 桌面版") {
		t.Fatalf("initialize should carry the usage instructions: %s", out.String())
	}
}
