package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"x-mem/internal/mcp"
)

// cmdInstall wires x-mem into the agents: hooks, the MCP server and, where
// the agent reads one, a short instruction block. All three agents read and
// write the same ~/.x-mem, which is what makes memory shared between them.
//
// Without --apply it only says what it would change. With --apply every file
// it touches is backed up first, earlier x-mem entries are replaced (so
// running it twice is harmless) and everything else in the file stays.
// --remove takes x-mem out again.
func cmdInstall(args []string) error {
	usage := errors.New("用法：x-mem install claude-code|codex|antigravity|claude-desktop|all [--apply] [--remove] [--drop]")
	if len(args) == 0 {
		return usage
	}
	target := args[0]
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	apply := fs.Bool("apply", false, "直接修改配置（先备份）")
	remove := fs.Bool("remove", false, "把 x-mem 从这个应用里拿掉")
	dropKR := fs.Bool("drop-old-mem", false, "顺手把 旧记忆库 的钩子和 MCP 从这些应用的配置里拿掉（不删 旧记忆库 自己的文件）")
	afterQuit := fs.Bool("after-quit", false, "（内部用）等 Claude 桌面版退出后再改它的配置")
	fs.Parse(args[1:])

	exe, err := os.Executable()
	if err != nil {
		exe = "x-mem"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	in := installer{exe: exe, apply: *apply || *remove, remove: *remove, dropLegacy: *dropKR, afterQuit: *afterQuit}
	var targets []string
	switch target {
	case "claude-code", "claude":
		targets = []string{"claude-code"}
	case "codex":
		targets = []string{"codex"}
	case "antigravity":
		targets = []string{"antigravity"}
	case "claude-desktop", "desktop":
		targets = []string{"claude-desktop"}
	case "all":
		targets = detectClients()
		if len(targets) == 0 {
			return errors.New("没找到 Claude Code、Codex 或 Antigravity")
		}
		fmt.Println("找到：", strings.Join(targets, "、"))
	default:
		return usage
	}
	for _, t := range targets {
		fmt.Printf("\n== %s\n", t)
		var err error
		switch t {
		case "claude-code":
			err = in.claude()
		case "codex":
			err = in.codex()
		case "antigravity":
			err = in.antigravity()
		case "claude-desktop":
			err = in.claudeDesktop()
		}
		if err != nil {
			fmt.Println("✗", err)
		}
	}
	if !in.apply {
		fmt.Println("\n以上是要做的改动；加 --apply 才真的改（改之前先备份）。")
	} else if !in.remove {
		fmt.Println("\n重开这些应用后生效；运行 x-mem doctor 确认。")
	}
	return nil
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// detectClients lists the agents installed on this machine.
func detectClients() []string {
	var out []string
	if exists(claudeDir()) || onPath("claude") {
		out = append(out, "claude-code")
	}
	if exists(codexDir()) || onPath("codex") {
		out = append(out, "codex")
	}
	if exists(filepath.Join(home(), ".gemini")) || onPath("agy") || onPath("antigravity") {
		out = append(out, "antigravity")
	}
	if claudeDesktopInstalled() {
		out = append(out, "claude-desktop")
	}
	return out
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".claude")
}

func codexDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".codex")
}

func geminiConfigDir() string {
	if d := os.Getenv("X_MEM_GEMINI_CONFIG"); d != "" {
		return d
	}
	return filepath.Join(home(), ".gemini", "config")
}

func settingsPath() string { return filepath.Join(claudeDir(), "settings.json") }

type installer struct {
	exe                                   string
	apply, remove, dropLegacy, afterQuit bool
}

func (in installer) say(format string, a ...any) { fmt.Printf("  "+format+"\n", a...) }

func (in installer) hookCmd(event, client string) map[string]any {
	cmd := shellQuote(in.exe) + " hook " + event
	if client != "" {
		cmd += " --client " + client
	}
	return map[string]any{"type": "command", "command": cmd, "timeout": 5}
}

// claude: hooks in settings.json, MCP through `claude mcp`.
func (in installer) claude() error {
	group := func(event string) map[string]any {
		return map[string]any{"hooks": []any{in.hookCmd(event, "")}}
	}
	guard := group("guard")
	guard["matcher"] = "*"
	ours := map[string]any{
		"SessionStart": group("start"), "UserPromptSubmit": group("prompt"),
		"PreToolUse": guard, "Stop": group("stop"),
	}
	if err := in.hooksFile(settingsPath(), ours); err != nil {
		return err
	}
	if err := in.mcpCLI("claude", []string{"mcp", "add", "--scope", "user", "x-mem", "--", in.exe, "mcp"},
		[]string{"mcp", "remove", "--scope", "user", "x-mem"}); err != nil {
		return err
	}
	if in.dropLegacy && in.apply && !in.remove {
		var cfg struct {
			Servers map[string]any `json:"mcpServers"`
		}
		raw, _ := os.ReadFile(filepath.Join(home(), ".claude.json"))
		_ = json.Unmarshal(raw, &cfg)
		for name, v := range cfg.Servers {
			if strings.Contains(strings.ToLower(name), "old-mem") || mentionsLegacy(v) {
				in.dropMCP("claude", name, []string{"mcp", "remove", "--scope", "user", name})
			}
		}
	}
	return nil
}

// dropMCP removes another tool's MCP server through the agent's own command.
func (in installer) dropMCP(bin, name string, args []string) {
	path, err := exec.LookPath(bin)
	if err != nil {
		in.say("旧记忆库 的 MCP（%s）请手动去掉：%s %s", name, bin, strings.Join(quoteAll(args), " "))
		return
	}
	if out, err := exec.Command(path, args...).CombinedOutput(); err != nil {
		in.say("旧记忆库 的 MCP（%s）没去掉：%s", name, strings.TrimSpace(string(out)))
		return
	}
	in.say("旧记忆库 的 MCP 已去掉：%s", name)
}

var tomlSection = regexp.MustCompile(`(?m)^\[mcp_servers\.("?)([^\]"]+)("?)\]\s*$`)

// codexMatchingServers lists the MCP servers in Codex's config.toml that run
// 旧记忆库.
func codexMatchingServers(toml string) []string {
	var names []string
	locs := tomlSection.FindAllStringSubmatchIndex(toml, -1)
	for i, loc := range locs {
		name := toml[loc[4]:loc[5]]
		end := len(toml)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if next := strings.Index(toml[loc[1]:end], "\n["); next >= 0 {
			end = loc[1] + next
		}
		body := strings.ToLower(name + toml[loc[1]:end])
		if strings.Contains(body, "old-mem") {
			names = append(names, name)
		}
	}
	return names
}

// codex: the same hook format in ~/.codex/hooks.json, MCP through `codex
// mcp`, and a block in ~/.codex/AGENTS.md. Codex runs a new hook only after
// you trust it with /hooks.
func (in installer) codex() error {
	group := func(event string) map[string]any {
		return map[string]any{"hooks": []any{in.hookCmd(event, "codex")}}
	}
	ours := map[string]any{
		"SessionStart": group("start"), "UserPromptSubmit": group("prompt"),
		"PreToolUse": group("guard"), "Stop": group("stop"),
	}
	if err := in.hooksFile(filepath.Join(codexDir(), "hooks.json"), ours); err != nil {
		return err
	}
	if err := in.mcpCLI("codex", []string{"mcp", "add", "x-mem", "--", in.exe, "mcp"}, []string{"mcp", "remove", "x-mem"}); err != nil {
		return err
	}
	if in.dropLegacy && in.apply && !in.remove {
		raw, _ := os.ReadFile(filepath.Join(codexDir(), "config.toml"))
		for _, name := range codexMatchingServers(string(raw)) {
			in.dropMCP("codex", name, []string{"mcp", "remove", name})
		}
	}
	if err := in.block(filepath.Join(codexDir(), "AGENTS.md")); err != nil {
		return err
	}
	if in.apply && !in.remove {
		in.say("在 Codex 里信任 x-mem 的四个钩子：CLI 里输入 /hooks，App 里是 设置 → Hooks（Codex 只运行信任过的钩子；钩子位置变了要重新信任）")
	}
	return nil
}

// antigravity: a named block in ~/.gemini/config/hooks.json (SessionStart,
// PreInvocation in place of prompt-submit, PreToolUse, Stop), the MCP server
// in ~/.gemini/config/mcp_config.json, and a block in
// ~/.gemini/config/AGENTS.md.
func (in installer) antigravity() error {
	dir := geminiConfigDir()
	ours := map[string]any{
		"SessionStart":  []any{in.hookCmd("start", "antigravity")},
		"PreInvocation": []any{in.hookCmd("prompt", "antigravity")},
		"PreToolUse":    []any{map[string]any{"matcher": ".*", "hooks": []any{in.hookCmd("guard", "antigravity")}}},
		"Stop":          []any{in.hookCmd("stop", "antigravity")},
	}
	var hooks any = ours
	if in.remove {
		hooks = nil
	}
	if err := in.jsonKey(filepath.Join(dir, "hooks.json"), []string{"x-mem"}, hooks); err != nil {
		return err
	}
	var server any = map[string]any{"command": in.exe, "args": []string{"mcp"}}
	if in.remove {
		server = nil
	}
	if err := in.jsonKey(filepath.Join(dir, "mcp_config.json"), []string{"mcpServers", "x-mem"}, server); err != nil {
		return err
	}
	if in.dropLegacy && in.apply {
		in.dropMatchingEntries(dir)
	}
	return in.block(filepath.Join(dir, "AGENTS.md"))
}

// dropMatchingEntries removes 旧记忆库's MCP server and hook blocks from
// Antigravity's config: 旧记忆库's own uninstaller does not know Antigravity.
func (in installer) dropMatchingEntries(dir string) {
	isKR := func(name string, v json.RawMessage) bool {
		s := strings.ToLower(name + string(v))
		return strings.Contains(s, "old-mem") || strings.Contains(s, "mcp_server.py")
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "mcp_config.json")); err == nil {
		var cfg struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			for name, v := range cfg.Servers {
				if isKR(name, v) {
					in.jsonKey(filepath.Join(dir, "mcp_config.json"), []string{"mcpServers", name}, nil)
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "hooks.json")); err == nil {
		var blocks map[string]json.RawMessage
		if json.Unmarshal(raw, &blocks) == nil {
			for name, v := range blocks {
				if name != "x-mem" && isKR(name, v) {
					in.jsonKey(filepath.Join(dir, "hooks.json"), []string{name}, nil)
				}
			}
		}
	}
}

// mcpCLI registers the MCP server with an agent's own command.
func (in installer) mcpCLI(bin string, add, remove []string) error {
	cmdline := bin + " " + strings.Join(quoteAll(add), " ")
	if in.remove {
		cmdline = bin + " " + strings.Join(quoteAll(remove), " ")
	}
	path, err := exec.LookPath(bin)
	if !in.apply || err != nil {
		if err != nil && in.apply {
			in.say("没找到 %s 命令，MCP 请手动处理：%s", bin, cmdline)
		} else {
			in.say("MCP：%s", cmdline)
		}
		return nil
	}
	_ = exec.Command(path, remove...).Run() // re-register with the current path
	if in.remove {
		in.say("MCP 已移除：x-mem")
		return nil
	}
	if out, err := exec.Command(path, add...).CombinedOutput(); err != nil {
		in.say("MCP 注册没成功：%s；手动运行：%s", strings.TrimSpace(string(out)), cmdline)
		return nil
	}
	in.say("MCP 已注册：x-mem")
	return nil
}

// hooksFile merges our groups into a Claude-Code-style hooks file.
func (in installer) hooksFile(path string, ours map[string]any) error {
	if !in.apply {
		in.say("钩子：%s（SessionStart、UserPromptSubmit、PreToolUse、Stop）", path)
		return nil
	}
	if in.remove {
		ours = nil
	}
	backup, dropped, err := mergeHooks(path, ours, in.remove, in.dropLegacy)
	if err != nil {
		return err
	}
	if dropped > 0 {
		in.say("旧记忆库 的钩子已拿掉：%d 个", dropped)
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

// jsonKey sets (or, with nil, deletes) a nested key in a JSON file, keeping
// every other key and the order of the top level.
func (in installer) jsonKey(path string, keyPath []string, value any) error {
	if !in.apply {
		in.say("%s：%s", path, strings.Join(keyPath, "."))
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if value == nil && len(raw) == 0 {
		return nil
	}
	keys, vals, err := orderedObject(raw)
	if err != nil {
		return fmt.Errorf("%s 不是合法的 JSON，没有改动：%v", path, err)
	}
	top := keyPath[0]
	if len(keyPath) == 1 {
		if value == nil {
			delete(vals, top)
		} else {
			b, _ := json.Marshal(value)
			if _, ok := vals[top]; !ok {
				keys = append(keys, top)
			}
			vals[top] = b
		}
	} else {
		inner := map[string]any{}
		if v, ok := vals[top]; ok {
			if err := json.Unmarshal(v, &inner); err != nil {
				return fmt.Errorf("%s 里的 %s 读不懂，没有改动", path, top)
			}
		} else {
			keys = append(keys, top)
		}
		if value == nil {
			delete(inner, keyPath[1])
		} else {
			inner[keyPath[1]] = value
		}
		b, _ := json.Marshal(inner)
		vals[top] = b
	}
	backup, err := writeOrdered(path, raw, keys, vals)
	if err != nil {
		return err
	}
	if backup != "" {
		in.say("已备份：%s", backup)
	}
	if value == nil {
		in.say("已移除：%s 里的 %s", path, strings.Join(keyPath, "."))
	} else {
		in.say("已写入：%s 里的 %s", path, strings.Join(keyPath, "."))
	}
	return nil
}

const (
	blockStart = "<!-- x-mem:start（由 x-mem install 维护，改动会被覆盖） -->"
	blockEnd   = "<!-- x-mem:end -->"
)

// agentBlock tells an agent without x-mem's per-message hook how to use the
// shared memory.
var agentBlock = "## 用户的记忆库（x-mem）\n\n" + mcp.Instructions

// block writes (or removes) x-mem's section in an instructions file,
// leaving the rest of the file as it is.
func (in installer) block(path string) error {
	if !in.apply {
		in.say("说明：%s 里加一段 x-mem 用法", path)
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(raw)
	i, j := strings.Index(s, blockStart), strings.Index(s, blockEnd)
	if i >= 0 && j > i {
		s = strings.TrimRight(s[:i], "\n") + "\n" + strings.TrimLeft(s[j+len(blockEnd):], "\n")
		s = strings.TrimLeft(s, "\n")
	}
	if !in.remove {
		if strings.TrimSpace(s) != "" {
			s = strings.TrimRight(s, "\n") + "\n\n"
		}
		s += blockStart + "\n" + agentBlock + "\n" + blockEnd + "\n"
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
		in.say("说明已移除：%s", path)
	} else {
		in.say("说明已写入：%s", path)
	}
	return nil
}

// mergeHooks puts our hook groups into a hooks file whose top level has a
// "hooks" object (Claude Code's settings.json, Codex's hooks.json). Groups
// whose command runs "x-mem hook" are replaced (or, with remove, dropped);
// every other group and top-level key stays where it was.
//
// With dropLegacy, hooks that run 旧记忆库 are taken out too, one by one, so
// another tool's hook sharing a group with one stays.
func mergeHooks(path string, ours map[string]any, remove, dropLegacy bool) (backup string, dropped int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", 0, err
	}
	if remove && len(raw) == 0 {
		return "", 0, nil
	}
	keys, vals, err := orderedObject(raw)
	if err != nil {
		return "", 0, fmt.Errorf("%s 不是合法的 JSON，没有改动：%v", path, err)
	}
	hooks := map[string]any{}
	if v, ok := vals["hooks"]; ok {
		if err := json.Unmarshal(v, &hooks); err != nil {
			return "", 0, fmt.Errorf("%s 里的 hooks 读不懂，没有改动：%v", path, err)
		}
	} else {
		keys = append(keys, "hooks")
	}
	for event, list := range hooks {
		groups, ok := list.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range groups {
			if isOurHook(g) {
				continue
			}
			if dropLegacy {
				var n int
				if g, n = withoutLegacy(g); n > 0 {
					dropped += n
					if g == nil {
						continue
					}
				}
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	for event, group := range ours {
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, group)
	}
	hv, err := json.Marshal(hooks)
	if err != nil {
		return "", 0, err
	}
	vals["hooks"] = hv
	backup, err = writeOrdered(path, raw, keys, vals)
	return backup, dropped, err
}

// mentionsLegacy says whether a config entry belongs to 旧记忆库.
func mentionsLegacy(v any) bool {
	b, _ := json.Marshal(v)
	s := strings.ToLower(string(b))
	return strings.Contains(s, "old-mem")
}

// withoutLegacy takes 旧记忆库's hooks out of one hook group. It returns nil
// when nothing is left in the group, and how many hooks it took out.
func withoutLegacy(g any) (any, int) {
	m, ok := g.(map[string]any)
	if !ok {
		if mentionsLegacy(g) {
			return nil, 1
		}
		return g, 0
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		if _, isCmd := m["command"]; isCmd && mentionsLegacy(m) {
			return nil, 1
		}
		return g, 0
	}
	var kept []any
	for _, h := range list {
		if !mentionsLegacy(h) {
			kept = append(kept, h)
		}
	}
	n := len(list) - len(kept)
	if n == 0 {
		return g, 0
	}
	if len(kept) == 0 {
		return nil, n
	}
	m["hooks"] = kept
	return m, n
}

// writeOrdered writes a JSON object with its keys in the given order,
// backing up what was there.
func writeOrdered(path string, raw []byte, keys []string, vals map[string]json.RawMessage) (backup string, err error) {
	var buf bytes.Buffer
	buf.WriteString("{")
	n := 0
	for _, k := range keys {
		v, ok := vals[k]
		if !ok {
			continue
		}
		if n > 0 {
			buf.WriteString(",")
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(":")
		buf.Write(v)
		n++
	}
	buf.WriteString("}")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, buf.Bytes(), "", "  "); err != nil {
		return "", err
	}
	pretty.WriteString("\n")
	if bytes.Equal(pretty.Bytes(), raw) {
		return "", nil
	}
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

// isOurHook reports whether a hook group runs "x-mem hook …".
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
