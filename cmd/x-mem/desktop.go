package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Claude's desktop app has no hooks and no instructions file: x-mem reaches
// it as a local MCP server in claude_desktop_config.json, and the server's
// own instructions (sent when the app connects) tell Claude how to use it.
//
// The app keeps that file in memory while it runs and writes the whole of it
// back when it quits, so a change made while it is open is lost. When the
// app is running, x-mem leaves the file alone and waits in the background
// for the app to quit, then makes the change; the next start picks it up.

// claudeDesktopConfig is where the Claude desktop app keeps its local MCP
// servers.
func claudeDesktopConfig() string {
	if d := os.Getenv("X_MEM_CLAUDE_DESKTOP_CONFIG"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "Claude", "claude_desktop_config.json")
	}
	return filepath.Join(home(), "Library", "Application Support", "Claude", "claude_desktop_config.json")
}

// claudeDesktopInstalled: the app has made its settings folder.
func claudeDesktopInstalled() bool {
	return exists(filepath.Dir(claudeDesktopConfig()))
}

// claudeDesktopRunning says whether the desktop app is open. Only macOS is
// checked; elsewhere the file is written and the user is told to restart.
var claudeDesktopRunning = func() bool {
	if os.Getenv("X_MEM_CLAUDE_DESKTOP_CONFIG") != "" || runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command("pgrep", "-f", "Claude.app/Contents/MacOS/Claude").Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

func stateDir() string {
	h := os.Getenv("X_MEM_HOME")
	if h == "" {
		h = filepath.Join(home(), ".x-mem")
	}
	return filepath.Join(h, "state")
}

func desktopWatchFile() string { return filepath.Join(stateDir(), "desktop-pending.json") }

type desktopPending struct {
	PID    int    `json:"pid"`
	Remove bool   `json:"remove"`
	Drop   string `json:"drop,omitempty"`
	Since  string `json:"since"`
}

// loadDesktopPending returns the change waiting for the app to quit, if its
// watcher is still alive.
func loadDesktopPending() (desktopPending, bool) {
	var p desktopPending
	data, err := os.ReadFile(desktopWatchFile())
	if err != nil || json.Unmarshal(data, &p) != nil || p.PID == 0 {
		return p, false
	}
	return p, processAlive(p.PID)
}

func (in installer) claudeDesktop() error {
	path := claudeDesktopConfig()
	if !in.apply {
		in.say("MCP：%s 里的 mcpServers.x-mem", path)
		return nil
	}
	if claudeDesktopRunning() && !in.afterQuit {
		if p, alive := loadDesktopPending(); alive {
			_ = exec.Command("kill", strconv.Itoa(p.PID)).Run()
		}
		args := []string{"install", "claude-desktop", "--apply", "--after-quit"}
		if in.remove {
			args = append(args, "--remove")
		}
		if in.drop != "" {
			args = append(args, "--drop", in.drop)
		}
		pid := spawn(in.exe, args...)
		if pid > 0 {
			_ = os.MkdirAll(stateDir(), 0o755)
			data, _ := json.Marshal(desktopPending{PID: pid, Remove: in.remove, Drop: in.drop, Since: time.Now().Format(time.RFC3339)})
			_ = os.WriteFile(desktopWatchFile(), data, 0o644)
		}
		in.say("Claude 桌面版正开着：它退出时会把自己的设置整份写回，现在改会被覆盖。")
		in.say("已在后台等它退出后再改；下次打开 Claude 就接上了。想马上生效：⌘Q 退出 Claude 再打开。")
		return nil
	}
	if in.afterQuit {
		deadline := time.Now().Add(14 * 24 * time.Hour)
		for claudeDesktopRunning() {
			if time.Now().After(deadline) {
				return fmt.Errorf("等了两周 Claude 桌面版都没退出，没有改动")
			}
			time.Sleep(3 * time.Second)
		}
		defer os.Remove(desktopWatchFile())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var server any = map[string]any{"command": in.exe, "args": []string{"mcp"}}
	if in.remove {
		server = nil
	}
	if err := in.jsonKey(path, []string{"mcpServers", "x-mem"}, server); err != nil {
		return err
	}
	if in.drop != "" && !in.remove {
		for _, name := range desktopMatchingServers(path, in.drop) {
			_ = in.jsonKey(path, []string{"mcpServers", name}, nil)
		}
	}
	return nil
}

// desktopMatchingServers lists entries in the desktop app's config matching pattern.
func desktopMatchingServers(path, pattern string) []string {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil
	}
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &cfg)
	var out []string
	for name, v := range cfg.Servers {
		if name != "x-mem" && strings.Contains(strings.ToLower(name+string(v)), pattern) {
			out = append(out, name)
		}
	}
	return out
}

// doctorDesktop reports on the desktop app.
func doctorDesktop(ok, note, fail func(string, ...any)) {
	if !claudeDesktopInstalled() {
		return
	}
	raw, _ := os.ReadFile(claudeDesktopConfig())
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	_ = json.Unmarshal(raw, &cfg)
	_, has := cfg.Servers["x-mem"]
	pending, waiting := loadDesktopPending()
	switch {
	case has && !(waiting && pending.Remove):
		ok("Claude 桌面版：MCP 接好了")
	case waiting:
		note("Claude 桌面版：等它退出后自动接上（%s 起在等）；想马上生效就 ⌘Q 退出再打开", strings.Replace(pending.Since, "T", " ", 1)[:16])
	default:
		note("Claude 桌面版还没接入 → x-mem install claude-desktop --apply")
	}
}

var spawn = spawnDetached
