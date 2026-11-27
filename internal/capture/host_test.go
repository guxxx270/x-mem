package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"x-mem/internal/config"
	"x-mem/internal/transcript"
)

// fakeBin writes a shell script standing in for an agent's command line.
func fakeBin(t *testing.T, dir, name, script string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const answer = `[{"n":1,"keep":true,"kind":"decision","title":"包管理器","text":"项目统一用 pnpm。","quote":"以后统一用 pnpm"},{"n":2,"keep":false}]`

func TestHostsCodexThenAntigravityNeverClaude(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	was := systemHostDirs
	systemHostDirs = func(_, home string) []string { return []string{filepath.Join(home, ".local/bin")} }
	t.Cleanup(func() { systemHostDirs = was })
	fakeBin(t, bin, "claude", "echo nope")
	if _, _, _, ok := ResolveHost(nil); ok {
		t.Fatal("with only claude installed, nothing is picked by default")
	}
	fakeBin(t, bin, "agy", "echo")
	if kind, _, _, ok := ResolveHost(nil); !ok || kind != "antigravity" {
		t.Fatalf("agy alone: %s %v", kind, ok)
	}
	fakeBin(t, bin, "codex", "echo")
	if kind, _, _, ok := ResolveHost(nil); !ok || kind != "codex" {
		t.Fatalf("codex first: %s %v", kind, ok)
	}
	if kind, _, _, _ := ResolveHost([]string{"claude"}); kind != "claude" {
		t.Fatal("claude only when chosen")
	}
	if kind, b, _, ok := ResolveHost([]string{filepath.Join(bin, "agy")}); kind != "antigravity" || b != filepath.Join(bin, "agy") || !ok {
		t.Fatalf("a full path keeps its preset: %s %s", kind, b)
	}
}

func TestCodexHostRun(t *testing.T) {
	bin := t.TempDir()
	// codex exec … -o <file> - : the prompt comes on stdin, the answer goes to the -o file.
	codex := fakeBin(t, bin, "codex", `
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
[ "$1" = "exec" ] || exit 3
prompt=$(cat)
case "$prompt" in *"`+transcript.HostMarker+`"*) ;; *) exit 4;; esac
[ -n "$X_MEM_DISABLE" ] || exit 5
echo 'progress on stdout' 
printf '%s' '`+answer+`' > "$out"
`)
	l := config.Layers{Home: t.TempDir()}
	l.State = filepath.Join(l.Home, "state")
	cands := []Candidate{
		{Sentence: "以后统一用 pnpm", Message: "以后统一用 pnpm，别用 npm", Session: "s"},
		{Sentence: "帮我看下这个函数", Message: "帮我看下这个函数"},
	}
	props, err := askHost(l, []string{codex}, cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || props[0].Text != "项目统一用 pnpm。" || props[0].Quote != "以后统一用 pnpm" {
		t.Fatalf("props: %+v", props)
	}
}

func TestAntigravityHostRun(t *testing.T) {
	bin := t.TempDir()
	agy := fakeBin(t, bin, "agy", `[ "$1" = "-p" ] || exit 3
case "$2" in *"`+transcript.HostMarker+`"*) ;; *) exit 4;; esac
printf '\033[32m%s\033[0m\n' '`+answer+`'
`)
	l := config.Layers{Home: t.TempDir()}
	l.State = filepath.Join(l.Home, "state")
	props, err := askHost(l, []string{agy}, []Candidate{{Sentence: "以后统一用 pnpm", Message: "以后统一用 pnpm"}, {Sentence: "x", Message: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || !strings.Contains(props[0].Text, "pnpm") {
		t.Fatalf("props: %+v", props)
	}
}

// The host's own session holds the prompt with the candidates: it must never
// be read back as something the user said.
func TestHostSessionIsNotTheUser(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + transcript.HostMarker + ` 以后统一用 pnpm"}]}}` + "\n"
	os.WriteFile(p, []byte(line), 0o644)
	msgs, _, err := transcript.ReadFrom(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "user" {
			t.Fatalf("host prompt read as a user message: %+v", m)
		}
	}
	if len(msgs) != 1 || !msgs[0].Headless {
		t.Fatalf("want one headless meta, got %+v", msgs)
	}
}
