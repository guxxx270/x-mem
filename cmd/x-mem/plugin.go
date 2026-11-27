package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"x-mem/internal/mcp"
)

// pluginInstalled says whether the x-mem plugin (plugin/ in the repository)
// is installed and switched on in an app. Then the app already has the hooks
// and the MCP server, and x-mem install must not add a second set.
func pluginInstalled(app string) bool {
	switch app {
	case "claude-code":
		var s struct {
			EnabledPlugins map[string]any `json:"enabledPlugins"`
		}
		data, _ := os.ReadFile(settingsPath())
		json.Unmarshal(data, &s)
		for k, v := range s.EnabledPlugins {
			if strings.HasPrefix(k, "x-mem@") && v == true {
				return true
			}
		}
	case "codex":
		data, _ := os.ReadFile(filepath.Join(codexDir(), "config.toml"))
		return codexPluginEnabled(string(data))
	case "antigravity":
		for _, d := range []string{
			filepath.Join(geminiConfigDir(), "plugins", "x-mem"),
			filepath.Join(home(), ".gemini", "antigravity-cli", "plugins", "x-mem"),
		} {
			if exists(d) {
				return true
			}
		}
	}
	return false
}

// codexPluginEnabled finds [plugins."x-mem@<marketplace>"] in Codex's
// config.toml, the table Codex writes when the plugin is installed, and
// checks that it is not switched off.
func codexPluginEnabled(toml string) bool {
	in, on := false, false
	sc := bufio.NewScanner(strings.NewReader(toml))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = strings.HasPrefix(line, `[plugins."x-mem@`)
			if in {
				on = true
			}
			continue
		}
		if in && strings.ReplaceAll(line, " ", "") == "enabled=false" {
			on = false
		}
	}
	return on
}

// keepStamp records the running version next to the stable program when this
// is it, so that the plugin's launcher never replaces a newer x-mem someone
// built or installed by hand with the older one it carries.
func keepStamp() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dst, err := filepath.EvalSymlinks(stableBinary())
	if err != nil || dst != exe {
		return
	}
	stamp := filepath.Join(filepath.Dir(stableBinary()), ".version")
	if data, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(data)) == mcp.Version {
		return
	}
	os.WriteFile(stamp, []byte(mcp.Version+"\n"), 0o644)
}
