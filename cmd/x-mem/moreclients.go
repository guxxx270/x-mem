package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func dshDir() string {
	if d := os.Getenv("DSH_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".dsh")
}

func cursorDir() string {
	if d := os.Getenv("X_MEM_CURSOR_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".cursor")
}

func grokDir() string {
	if d := os.Getenv("GROK_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".grok")
}

const (
	yamlBlockStart = "# x-mem:start（由 x-mem install 维护，改动会被覆盖）"
	yamlBlockEnd   = "# x-mem:end"
)

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// dsh wires x-mem into DeepSeek Harness (@deepseek-ai/dsh): a Claude-compatible
// hooks file at ~/.dsh/x-mem-hooks.json, mounted alongside @deepseek-ai/dsh-mcp-client
// in the home-layer ~/.dsh/cordis.patch.yml, plus ~/.dsh/AGENTS.md.
func (in installer) dsh() error {
	dir := dshDir()
	hooksPath := filepath.Join(dir, "x-mem-hooks.json")
	patchPath := filepath.Join(dir, "cordis.patch.yml")
	agentsPath := filepath.Join(dir, "AGENTS.md")

	if !in.apply {
		in.say("钩子：%s（SessionStart、UserPromptSubmit、PreToolUse、Stop）", hooksPath)
		in.say("Cordis 补丁：%s（挂载 mcp-x-mem 与 hooks-x-mem）", patchPath)
		in.say("说明：%s 里加一段 x-mem 用法", agentsPath)
		return nil
	}

	if in.remove {
		if exists(hooksPath) {
			_ = os.Remove(hooksPath)
			in.say("钩子已移除：%s", hooksPath)
		}
	} else {
		group := func(event string) []any {
			return []any{map[string]any{"hooks": []any{in.hookCmd(event, "dsh")}}}
		}
		guard := []any{map[string]any{"matcher": "*", "hooks": []any{in.hookCmd("guard", "dsh")}}}
		payload := map[string]any{
			"hooks": map[string]any{
				"SessionStart":     group("start"),
				"UserPromptSubmit": group("prompt"),
				"PreToolUse":       guard,
				"Stop":             group("stop"),
			},
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		if err := os.WriteFile(hooksPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
		in.say("钩子已写入：%s", hooksPath)
	}

	patch := fmt.Sprintf(`- insert:
    - id: mcp-x-mem
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        serverName: x-mem
        transport: stdio
        command: %s
        args:
          - mcp
    - id: hooks-x-mem
      name: '@deepseek-ai/dsh-hooks-claude-code'
      config:
        settingsPath: %s`, yamlQuote(in.exe), yamlQuote(hooksPath))

	if err := in.yamlBlock(patchPath, patch); err != nil {
		return err
	}
	return in.block(agentsPath)
}

func (in installer) yamlBlock(path, content string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(raw)
	i, j := strings.Index(s, yamlBlockStart), strings.Index(s, yamlBlockEnd)
	if i >= 0 && j > i {
		s = strings.TrimRight(s[:i], "\n") + "\n" + strings.TrimLeft(s[j+len(yamlBlockEnd):], "\n")
		s = strings.TrimLeft(s, "\n")
	}
	if !in.remove {
		if strings.TrimSpace(s) != "" {
			s = strings.TrimRight(s, "\n") + "\n\n"
		}
		s += yamlBlockStart + "\n" + content + "\n" + yamlBlockEnd + "\n"
	} else {
		s = strings.TrimSpace(s)
		if s != "" {
			s += "\n"
		}
	}
	if string(raw) == s {
		return nil
	}
	if len(raw) > 0 {
		backup := path + ".x-mem-bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return err
		}
		in.say("已备份：%s", backup)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return err
	}
	if in.remove {
		in.say("Cordis 补丁已移除：%s", path)
	} else {
		in.say("Cordis 补丁已写入：%s", path)
	}
	return nil
}

// cursor wires x-mem into Cursor IDE: ~/.cursor/hooks.json, ~/.cursor/mcp.json
// and an always-applied rule at ~/.cursor/rules/x-mem.mdc.
func (in installer) cursor() error {
	dir := cursorDir()
	hooksPath := filepath.Join(dir, "hooks.json")
	mcpPath := filepath.Join(dir, "mcp.json")
	rulePath := filepath.Join(dir, "rules", "x-mem.mdc")

	if !in.apply {
		in.say("钩子：%s（sessionStart、beforeSubmitPrompt、beforeShellExecution、stop）", hooksPath)
		in.say("MCP：%s 里的 mcpServers.x-mem", mcpPath)
		in.say("规则：%s", rulePath)
		return nil
	}

	if err := in.cursorHooks(hooksPath); err != nil {
		return err
	}
	var server any = map[string]any{"command": in.exe, "args": []string{"mcp"}}
	if in.remove {
		server = nil
	}
	if err := in.jsonKey(mcpPath, []string{"mcpServers", "x-mem"}, server); err != nil {
		return err
	}
	if in.drop != "" && !in.remove {
		for _, name := range desktopMatchingServers(mcpPath, in.drop) {
			_ = in.jsonKey(mcpPath, []string{"mcpServers", name}, nil)
		}
	}
	if in.remove {
		if exists(rulePath) {
			_ = os.Remove(rulePath)
			in.say("规则已移除：%s", rulePath)
		}
		return nil
	}
	mdc := "---\ndescription: x-mem 跨端共享本地记忆\nalwaysApply: true\n---\n\n" + agentBlock + "\n"
	if old, err := os.ReadFile(rulePath); err == nil && string(old) == mdc {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rulePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(rulePath, []byte(mdc), 0o644); err != nil {
		return err
	}
	in.say("规则已写入：%s", rulePath)
	return nil
}

func (in installer) cursorHooks(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if in.remove && len(raw) == 0 {
		return nil
	}
	keys, vals, err := orderedObject(raw)
	if err != nil {
		return fmt.Errorf("%s 不是合法的 JSON，没有改动：%v", path, err)
	}
	if _, ok := vals["version"]; !ok && !in.remove {
		keys = append([]string{"version"}, keys...)
		vals["version"] = json.RawMessage("1")
	}
	hooks := map[string]any{}
	if v, ok := vals["hooks"]; ok {
		if err := json.Unmarshal(v, &hooks); err != nil {
			return fmt.Errorf("%s 里的 hooks 读不懂，没有改动：%v", path, err)
		}
	} else if !in.remove {
		keys = append(keys, "hooks")
	}
	pat := strings.ToLower(strings.TrimSpace(in.drop))
	dropped := 0
	for ev, list := range hooks {
		arr, ok := list.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, item := range arr {
			if isOurHook(item) {
				continue
			}
			if pat != "" && mentionsTool(item, pat) {
				dropped++
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if !in.remove {
		entry := func(ev string) map[string]any {
			return map[string]any{
				"command": shellQuote(in.exe) + " hook " + ev + " --client cursor",
				"timeout": 5,
			}
		}
		ours := map[string]map[string]any{
			"sessionStart":         entry("start"),
			"beforeSubmitPrompt":   entry("prompt"),
			"beforeShellExecution": entry("guard"),
			"stop":                 entry("stop"),
		}
		for ev, item := range ours {
			arr, _ := hooks[ev].([]any)
			hooks[ev] = append(arr, item)
		}
	}
	hv, _ := json.Marshal(hooks)
	vals["hooks"] = hv
	backup, err := writeOrdered(path, raw, keys, vals)
	if err != nil {
		return err
	}
	if dropped > 0 {
		in.say("旧工具的钩子已拿掉：%d 个", dropped)
	}
	if backup != "" {
		in.say("已备份：%s", backup)
	}
	if in.remove {
		in.say("钩子已移除：%s", path)
	} else {
		in.say("钩子已写入：%s", path)
	}
	return nil
}

// grok wires x-mem into xAI Grok CLI (~/.grok): standalone ~/.grok/hooks/x-mem.json,
// MCP registration in ~/.grok/config.toml (via `grok mcp` when on PATH, or direct
// TOML edit), and ~/.grok/AGENTS.md.
func (in installer) grok() error {
	dir := grokDir()
	hooksPath := filepath.Join(dir, "hooks", "x-mem.json")
	cfgPath := filepath.Join(dir, "config.toml")
	agentsPath := filepath.Join(dir, "AGENTS.md")

	if !in.apply {
		in.say("钩子：%s（SessionStart、UserPromptSubmit、PreToolUse、Stop）", hooksPath)
		in.say("MCP：%s 里的 [mcp_servers.x-mem]", cfgPath)
		in.say("说明：%s 里加一段 x-mem 用法", agentsPath)
		return nil
	}

	if in.remove {
		if exists(hooksPath) {
			_ = os.Remove(hooksPath)
			in.say("钩子已移除：%s", hooksPath)
		}
	} else {
		group := func(event string) []any {
			return []any{map[string]any{"hooks": []any{in.hookCmd(event, "grok")}}}
		}
		guard := []any{map[string]any{"matcher": "*", "hooks": []any{in.hookCmd("guard", "grok")}}}
		payload := map[string]any{
			"hooks": map[string]any{
				"SessionStart":     group("start"),
				"UserPromptSubmit": group("prompt"),
				"PreToolUse":       guard,
				"Stop":             group("stop"),
			},
		}
		if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		if err := os.WriteFile(hooksPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
		in.say("钩子已写入：%s", hooksPath)
	}

	if os.Getenv("GROK_HOME") == "" {
		if _, err := exec.LookPath("grok"); err == nil {
			if err := in.mcpCLI("grok", []string{"mcp", "add", "x-mem", "--", in.exe, "mcp"}, []string{"mcp", "remove", "x-mem"}); err != nil {
				return err
			}
			if in.drop != "" && !in.remove {
				raw, _ := os.ReadFile(cfgPath)
				for _, name := range codexMatchingServers(string(raw), in.drop) {
					in.dropMCP("grok", name, []string{"mcp", "remove", name})
				}
			}
			return in.block(agentsPath)
		}
	}
	if err := in.tomlMCP(cfgPath); err != nil {
		return err
	}
	return in.block(agentsPath)
}

// tomlMCP sets or removes [mcp_servers.x-mem] (and optional --drop sections)
// in a config.toml file when the agent's CLI is not on PATH.
func (in installer) tomlMCP(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if in.remove && len(raw) == 0 {
		return nil
	}
	s := removeTOMLMCP(string(raw), "x-mem")
	if in.drop != "" && !in.remove {
		for _, name := range codexMatchingServers(s, in.drop) {
			s = removeTOMLMCP(s, name)
		}
	}
	if !in.remove {
		if strings.TrimSpace(s) != "" {
			s = strings.TrimRight(s, "\n") + "\n\n"
		} else {
			s = ""
		}
		s += fmt.Sprintf("[mcp_servers.x-mem]\ncommand = %s\nargs = [\"mcp\"]\n", yamlQuote(in.exe))
	}
	if string(raw) == s {
		return nil
	}
	if len(raw) > 0 {
		backup := path + ".x-mem-bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return err
		}
		in.say("已备份：%s", backup)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return err
	}
	if in.remove {
		in.say("MCP 已移除：%s", path)
	} else {
		in.say("MCP 已写入：%s", path)
	}
	return nil
}

func removeTOMLMCP(toml, target string) string {
	locs := tomlSection.FindAllStringSubmatchIndex(toml, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		name := toml[loc[4]:loc[5]]
		if name != target {
			continue
		}
		end := len(toml)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if next := strings.Index(toml[loc[1]:end], "\n["); next >= 0 {
			end = loc[1] + next + 1
		}
		toml = strings.TrimRight(toml[:loc[0]], "\n") + "\n" + strings.TrimLeft(toml[end:], "\n")
	}
	return strings.TrimSpace(toml) + "\n"
}

func doctorExtraClients(installed map[string]bool, ok, note, fail func(string, ...any)) {
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if installed["DSH"] {
		patch := read(filepath.Join(dshDir(), "cordis.patch.yml"))
		hooks := read(filepath.Join(dshDir(), "x-mem-hooks.json"))
		hasMCP := strings.Contains(patch, "mcp-x-mem")
		hasHooks := strings.Contains(patch, "hooks-x-mem") && strings.Contains(hooks, "x-mem hook start")
		switch {
		case hasMCP && hasHooks:
			ok("DSH：钩子和 MCP 都接好了")
		case !hasMCP && !hasHooks:
			note("DSH 还没接入 → x-mem install dsh --apply")
		default:
			fail("DSH：配置不完整 → x-mem install dsh --apply")
		}
	}
	if installed["Cursor"] {
		hooks := read(filepath.Join(cursorDir(), "hooks.json"))
		mcp := read(filepath.Join(cursorDir(), "mcp.json"))
		hasHooks := strings.Contains(hooks, "x-mem hook start --client cursor") &&
			strings.Contains(hooks, "x-mem hook guard --client cursor")
		hasMCP := strings.Contains(mcp, "x-mem")
		switch {
		case hasHooks && hasMCP:
			ok("Cursor：钩子和 MCP 都接好了")
		case !strings.Contains(hooks, "x-mem") && !hasMCP:
			note("Cursor 还没接入 → x-mem install cursor --apply")
		default:
			fail("Cursor：配置不完整 → x-mem install cursor --apply")
		}
	}
	if installed["Grok CLI"] {
		hooks := read(filepath.Join(grokDir(), "hooks", "x-mem.json"))
		mcp := read(filepath.Join(grokDir(), "config.toml"))
		hasHooks := strings.Contains(hooks, "x-mem hook start --client grok")
		hasMCP := strings.Contains(mcp, "x-mem")
		switch {
		case hasHooks && hasMCP:
			ok("Grok CLI：钩子和 MCP 都接好了")
		case !hasHooks && !hasMCP:
			note("Grok CLI 还没接入 → x-mem install grok --apply")
		default:
			fail("Grok CLI：配置不完整 → x-mem install grok --apply")
		}
	}
}
