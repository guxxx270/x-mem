package main

import (
	"reflect"
	"testing"
)

// A hooks.json shaped like a real one: another tool's hook and 旧记忆库's sit
// before x-mem's in each event, so x-mem's hooks are at group 1 or 2.
const codexHooks = `{"hooks": {
  "PreToolUse": [{"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook guard --client codex", "type": "command"}]}],
  "SessionStart": [
    {"hooks": [{"command": "OtherTool"}], "matcher": "startup|resume"},
    {"hooks": [{"command": "python3 old-mem/session_hook.py start"}]},
    {"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook start --client codex"}]}
  ],
  "Stop": [
    {"hooks": [{"command": "OtherTool"}]},
    {"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook stop --client codex"}]}
  ],
  "UserPromptSubmit": [
    {"hooks": [{"command": "OtherTool"}]},
    {"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook prompt --client codex"}]}
  ]
}}`

func TestCodexUntrusted(t *testing.T) {
	path := "/Users/u/.codex/hooks.json"
	// Only the other tool's hooks (group 0) were trusted.
	onlyOthers := `[features]
hooks = true

[hooks.state]

[hooks.state."/Users/u/.codex/hooks.json:session_start:0:0"]
trusted_hash = "sha256:aa"

[hooks.state."/Users/u/.codex/hooks.json:stop:0:0"]
trusted_hash = "sha256:bb"
`
	got := codexUntrusted(path, codexHooks, onlyOthers)
	want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("untrusted = %v, want %v", got, want)
	}
	all := onlyOthers + `
[hooks.state."/Users/u/.codex/hooks.json:session_start:2:0"]
trusted_hash = "sha256:cc"

[hooks.state."/Users/u/.codex/hooks.json:user_prompt_submit:1:0"]
trusted_hash = "sha256:dd"

[hooks.state."/Users/u/.codex/hooks.json:pre_tool_use:0:0"]
trusted_hash = "sha256:ee"

[hooks.state."/Users/u/.codex/hooks.json:stop:1:0"]
trusted_hash = "sha256:ff"
`
	if got := codexUntrusted(path, codexHooks, all); len(got) != 0 {
		t.Fatalf("all trusted, still reported: %v", got)
	}
	// A Codex that keeps no trust records: nothing to report.
	if got := codexUntrusted(path, codexHooks, "[features]\nhooks = true\n"); got != nil {
		t.Fatalf("no trust records, reported %v", got)
	}
}
