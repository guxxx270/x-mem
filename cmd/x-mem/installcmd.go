package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cmdInstall wires x-mem into Claude Code: two hooks in settings.json and
// the MCP server. Without --apply it only prints what to add. With --apply
// it backs up settings.json, replaces any earlier x-mem hooks with the
// current ones (so running it twice is harmless), keeps every other key in
// its place, and registers the MCP server through the claude command.
func cmdInstall(args []string) error {
	if len(args) == 0 || args[0] != "claude-code" {
		return fmt.Errorf("用法：x-mem install claude-code [--apply]")
	}
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	apply := fs.Bool("apply", false, "直接修改 Claude Code 配置（先备份）")
	fs.Parse(args[1:])

	exe, err := os.Executable()
	if err != nil {
		exe = "x-mem"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	hookCmd := func(ev string) map[string]any {
		return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": shellQuote(exe) + " hook " + ev, "timeout": 5}}}
	}
	guardGroup := hookCmd("guard")
	guardGroup["matcher"] = "*"
	ours := map[string]any{
		"SessionStart":     hookCmd("start"),
		"UserPromptSubmit": hookCmd("prompt"),
		"PreToolUse":       guardGroup,
		"Stop":             hookCmd("stop"),
	}
	mcpArgs := []string{"mcp", "add", "--scope", "user", "x-mem", "--", exe, "mcp"}

	if !*apply {
		hooks := map[string]any{}
		for k, v := range ours {
			hooks[k] = []any{v}
		}
		data, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
		fmt.Println("1. 把下面的 hooks 合并进 " + settingsPath() + "（已有 hooks 时只加这四项，别覆盖）：")
		fmt.Println()
		fmt.Println(string(data))
		fmt.Println()
		fmt.Println("2. 注册 MCP 服务：")
		fmt.Println()
		fmt.Println("   claude " + strings.Join(quoteAll(mcpArgs), " "))
		fmt.Println()
		fmt.Println("3. 重开 Claude Code，运行 x-mem doctor 确认。")
		fmt.Println("\n或者直接运行 x-mem install claude-code --apply（会先备份 settings.json）。")
		return nil
	}

	path := settingsPath()
	backup, err := mergeHooks(path, ours)
	if err != nil {
		return err
	}
	if backup != "" {
		fmt.Println("已备份：", backup)
	}
	fmt.Println("钩子已写入：", path)

	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("没找到 claude 命令，MCP 请手动注册：claude " + strings.Join(quoteAll(mcpArgs), " "))
		return nil
	}
	_ = exec.Command(claude, "mcp", "remove", "--scope", "user", "x-mem").Run() // re-register with the current path
	if out, err := exec.Command(claude, mcpArgs...).CombinedOutput(); err != nil {
		fmt.Printf("MCP 注册没成功：%s\n手动运行：claude %s\n", strings.TrimSpace(string(out)), strings.Join(quoteAll(mcpArgs), " "))
		return nil
	}
	fmt.Println("MCP 已注册：x-mem（用户级）")
	fmt.Println("重开 Claude Code 后生效；运行 x-mem doctor 确认。")
	return nil
}

func settingsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

// mergeHooks puts our hook groups into settings.json. Top-level keys keep
// their order; inside "hooks", groups whose command runs "x-mem hook" are
// replaced and every other group stays.
func mergeHooks(path string, ours map[string]any) (backup string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	keys, vals, err := orderedObject(raw)
	if err != nil {
		return "", fmt.Errorf("%s 不是合法的 JSON，没有改动：%v", path, err)
	}
	hooks := map[string]any{}
	if v, ok := vals["hooks"]; ok {
		if err := json.Unmarshal(v, &hooks); err != nil {
			return "", fmt.Errorf("%s 里的 hooks 读不懂，没有改动：%v", path, err)
		}
	} else {
		keys = append(keys, "hooks")
	}
	for event, group := range ours {
		var kept []any
		if list, ok := hooks[event].([]any); ok {
			for _, g := range list {
				if !isOurHook(g) {
					kept = append(kept, g)
				}
			}
		}
		hooks[event] = append(kept, group)
	}
	hv, err := json.Marshal(hooks)
	if err != nil {
		return "", err
	}
	vals["hooks"] = hv

	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(":")
		buf.Write(vals[k])
	}
	buf.WriteString("}")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, buf.Bytes(), "", "  "); err != nil {
		return "", err
	}
	pretty.WriteString("\n")

	if len(raw) > 0 {
		backup = path + ".x-mem-bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return backup, err
	}
	tmp := path + ".x-mem-tmp"
	if err := os.WriteFile(tmp, pretty.Bytes(), 0o600); err != nil {
		return backup, err
	}
	return backup, os.Rename(tmp, path)
}

// isOurHook reports whether a hook group runs "x-mem hook start|prompt".
func isOurHook(g any) bool {
	b, _ := json.Marshal(g)
	s := string(b)
	if !strings.Contains(s, "x-mem") {
		return false
	}
	for _, ev := range []string{" hook start", " hook prompt", " hook guard", " hook stop"} {
		if strings.Contains(s, ev) {
			return true
		}
	}
	return false
}

// orderedObject splits a JSON object into its keys, in file order, and raw
// values. Empty input is an empty object.
func orderedObject(raw []byte) ([]string, map[string]json.RawMessage, error) {
	vals := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, vals, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("最外层不是对象")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return keys, vals, nil
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`!*?&;|<>(){}[]") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = shellQuote(x)
	}
	return out
}
