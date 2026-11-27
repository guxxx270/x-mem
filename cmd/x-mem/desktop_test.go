package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeDesktopInstall(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "Claude", "claude_desktop_config.json")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte(`{"mcpServers":{"old-mem":{"command":"python3","args":["/x/.old-mem/mcp_server.py"]},"other":{"command":"z"}},"globalShortcut":"Alt+Space"}`), 0o644)
	t.Setenv("X_MEM_CLAUDE_DESKTOP_CONFIG", cfg)
	t.Setenv("X_MEM_HOME", filepath.Join(dir, "xm"))

	in := installer{exe: "/opt/x-mem", apply: true, dropLegacy: true}
	if err := in.claudeDesktop(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Servers  map[string]map[string]any `json:"mcpServers"`
		Shortcut string                    `json:"globalShortcut"`
	}
	raw, _ := os.ReadFile(cfg)
	json.Unmarshal(raw, &got)
	if got.Servers["x-mem"]["command"] != "/opt/x-mem" || got.Servers["other"] == nil || got.Servers["old-mem"] != nil || got.Shortcut != "Alt+Space" {
		t.Fatalf("config after install: %s", raw)
	}

	// While the app is open the file is left alone and a watcher waits.
	running := true
	was := claudeDesktopRunning
	claudeDesktopRunning = func() bool { return running }
	t.Cleanup(func() { claudeDesktopRunning = was })
	var spawned []string
	spawn = func(name string, args ...string) int { spawned = append([]string{name}, args...); return os.Getpid() }
	t.Cleanup(func() { spawn = spawnDetached })
	before, _ := os.ReadFile(cfg)
	in = installer{exe: "/opt/x-mem", apply: true, remove: true}
	if err := in.claudeDesktop(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Fatal("the config must not change while the app runs")
	}
	if !strings.Contains(strings.Join(spawned, " "), "install claude-desktop --apply --after-quit --remove") {
		t.Fatalf("watcher not started: %v", spawned)
	}
	if p, alive := loadDesktopPending(); !alive || !p.Remove {
		t.Fatalf("pending change not recorded: %+v %v", p, alive)
	}

	// The watcher, once the app has quit, makes the change.
	running = false
	in.afterQuit = true
	if err := in.claudeDesktop(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cfg)
	if strings.Contains(string(raw), "x-mem") {
		t.Fatalf("remove after quit: %s", raw)
	}
	if _, err := os.Stat(desktopWatchFile()); !os.IsNotExist(err) {
		t.Fatal("pending file should be gone once applied")
	}
}
