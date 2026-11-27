package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDropMatchingHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	before := `{"hooks": {
  "SessionStart": [
    {"hooks": [{"command": "OtherTool", "timeout": 45}], "matcher": "startup|resume"},
    {"hooks": [{"command": "/opt/anaconda3/bin/python3 /Users/u/.agents/skills/old-mem/scripts/session_hook.py start --agent codex"}]},
    {"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook start --client codex"}]}
  ],
  "SessionEnd": [
    {"hooks": [{"command": "python3 /Users/u/.agents/skills/old-mem/scripts/session_hook.py end"}]}
  ],
  "Stop": [
    {"hooks": [{"command": "OtherTool"}, {"command": "python3 ~/.old-mem/x/stop.py"}]}
  ]
}, "other": 1}`
	os.WriteFile(path, []byte(before), 0o644)
	ours := map[string]any{"SessionStart": map[string]any{"hooks": []any{map[string]any{"command": "/new/x-mem hook start --client codex"}}}}
	_, dropped, err := mergeHooks(path, ours, false, "old-mem")
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 3 {
		t.Fatalf("dropped %d old-mem hooks, want 3", dropped)
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if strings.Contains(strings.ToLower(s), "old-mem") {
		t.Fatalf("old-mem left behind:\n%s", s)
	}
	var got struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
			Matcher string `json:"matcher"`
		} `json:"hooks"`
		Other int `json:"other"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Other != 1 {
		t.Fatalf("other top-level keys must stay: %s", s)
	}
	if _, ok := got.Hooks["SessionEnd"]; ok {
		t.Fatalf("an event left empty is removed: %s", s)
	}
	ss := got.Hooks["SessionStart"]
	if len(ss) != 2 || ss[0].Hooks[0].Command != "OtherTool" || ss[0].Matcher != "startup|resume" || !strings.HasPrefix(ss[1].Hooks[0].Command, "/new/x-mem") {
		t.Fatalf("SessionStart should keep the other tool first and x-mem's new hook: %s", s)
	}
	if st := got.Hooks["Stop"]; len(st) != 1 || len(st[0].Hooks) != 1 || st[0].Hooks[0].Command != "OtherTool" {
		t.Fatalf("another tool sharing a group with old-mem stays: %s", s)
	}

	// Without --drop nothing of other tools is touched.
	os.WriteFile(path, []byte(before), 0o644)
	if _, dropped, _ := mergeHooks(path, ours, false, ""); dropped != 0 {
		t.Fatalf("dropped %d without the flag", dropped)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "old-mem") {
		t.Fatal("existing hooks must stay without --drop")
	}
}

func TestCodexMatchingServers(t *testing.T) {
	toml := `model = "gpt"

[mcp_servers.old-mem]
command = "/opt/anaconda3/bin/python3"
args = ["/Users/u/.old-mem/1.0/mcp_server.py"]

[mcp_servers.notes]
command = "/usr/bin/notes"

[mcp_servers."kb-old"]
command = "python3"
args = ["/Users/u/.agents/skills/old-mem/mcp.py"]

[mcp_servers.x-mem]
command = "/Users/u/.x-mem/bin/x-mem"
args = ["mcp"]

[features]
hooks = true
`
	got := codexMatchingServers(toml, "old-mem")
	want := []string{"old-mem", "kb-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
