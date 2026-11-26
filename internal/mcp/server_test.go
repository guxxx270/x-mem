package mcp

import (
	"bytes"
	"encoding/json"
	"os"
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
