package main

import (
	"reflect"
	"testing"
)

// A hooks.json shaped like a real one: two other tools' hooks sit
// before x-mem's in each event, so x-mem's hooks are at group 1 or 2.
const codexHooks = `{"hooks": {
  "PreToolUse": [{"hooks": [{"command": "/Users/u/.x-mem/bin/x-mem hook guard --client codex", "type": "command"}]}],
  "SessionStart": [
    {"hooks": [{"command": "OtherTool"}], "matcher": "startup|resume"},
    {"hooks": [{"command": "python3 legacy-tool/session_hook.py start"}]},
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

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.6.0", "0.5.1", true}, {"0.5.1", "0.6.0", false}, {"0.10.0", "0.9.9", true}, {"1.0", "1.0.0", false}, {"0.6.0", "", true}} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestCodexPluginEnabled(t *testing.T) {
	cases := map[string]bool{
		"": false,
		"[plugins.\"x-mem@x-mem\"]\nenabled = true\n":                   true,
		"[plugins.\"x-mem@team\"]\n":                                    true,
		"[plugins.\"x-mem@x-mem\"]\nenabled = false\n[mcp_servers.x]\n": false,
		"[plugins.\"gmail@openai-curated\"]\nenabled = true\n":          false,
		"[plugins.\"x-mem@x-mem\"]\n[other]\nenabled = false\n":         true,
	}
	for toml, want := range cases {
		if got := codexPluginEnabled(toml); got != want {
			t.Errorf("%q: got %v", toml, got)
		}
	}
}
