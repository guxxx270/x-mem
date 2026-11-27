// Command x-mem is a local memory for coding agents: Markdown notes in two
// layers (personal and per-project), found by a Chinese-aware index, handed to
// the agent through Claude Code hooks and an MCP server.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"x-mem/internal/capture"
	"x-mem/internal/config"
	"x-mem/internal/eval"
	"x-mem/internal/hook"
	"x-mem/internal/index"
	"x-mem/internal/inject"
	"x-mem/internal/mcp"
	"x-mem/internal/recall"
	"x-mem/internal/textutil"
	"x-mem/internal/writer"
)

const usage = `x-mem：给编程 agent 用的本地记忆

查：
  x-mem recall [--scope S] 问题   查记忆，第一行是 direct / related / none
  x-mem why 问题                  解释为什么是这个结果
  x-mem read <id|文件名|标题>     看一条笔记的全文和关系

写：
  x-mem remember [选项] 内容      记一条（--kind rule、--supersedes id [--line 原文]、--trigger 'Bash(git push*)'）
  x-mem inbox [review|show|approve [--all]|reject [--all]]  待确认的提案（review 逐条按键过，approve --all 全收）
  x-mem undo <run>                撤回一次写入
  x-mem log                       最近的写入
  x-mem capture on|off|status     会话自动采集（默认关）
  x-mem writer [--once]           写入进程（平时由钩子和定时任务拉起）

管理：
  x-mem init [--personal]         建个人层；在当前仓库里建项目层
  x-mem status / doctor           状态 / 体检
  x-mem reindex                   删掉索引重建（平时不需要）
  x-mem eval <题集.json>          跑回归题集
  x-mem import obsidian [库目录]   从 旧记忆库 库导入（--dry-run 先预演）
  x-mem kb add <目录> [--exclude 子目录]   把资料文件夹登记成知识库：能查、带出处（PDF 带页码），不算记忆
                                  认 Markdown、Word（docx/doc）、PDF、Excel（xlsx/xls）、PowerPoint（pptx/ppt）、
                                  WPS/LibreOffice（odt/ods/odp）、EPUB、RTF、HTML、txt/csv（含 GBK 编码）
  x-mem kb list / read / remove   看知识库（读不出的文件也列出来）/ 马上读完新文件 / 移除
  x-mem install claude-code|codex|antigravity|claude-desktop|all   接入这些应用（--apply 直接改配置，先备份；--remove 拿掉；--drop 顺手清掉 旧记忆库）
  x-mem hook start|prompt|guard|stop [--client codex|antigravity]   钩子（从标准输入读事件）
  x-mem mcp                       以 MCP 服务运行（stdio）
  x-mem version

环境变量：X_MEM_HOME 改个人层位置（默认 ~/.x-mem），X_MEM_PROJECT 指定项目层。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	// Documents added to the knowledge base are read in the background, so
	// that a hook or a query never waits on a PDF.
	index.OnPending = func(l config.Layers) {
		if exe, err := os.Executable(); err == nil && os.Getenv("X_MEM_NO_BACKGROUND") == "" {
			spawnDetached(exe, "kb", "read", "--quiet")
		}
	}
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "recall":
		err = cmdRecall(args)
	case "why":
		err = cmdWhy(args)
	case "read":
		err = cmdRead(args)
	case "status":
		err = cmdStatus()
	case "doctor":
		os.Exit(cmdDoctor())
	case "reindex":
		err = cmdReindex()
	case "eval":
		os.Exit(cmdEval(args))
	case "hook":
		client := hook.Claude
		var rest []string
		for i := 0; i < len(args); i++ {
			switch {
			case args[i] == "--client" && i+1 < len(args):
				client = args[i+1]
				i++
			case strings.HasPrefix(args[i], "--client="):
				client = strings.TrimPrefix(args[i], "--client=")
			default:
				rest = append(rest, args[i])
			}
		}
		if len(rest) != 1 {
			err = fmt.Errorf("用法：x-mem hook start|prompt|guard|stop [--client claude|codex|antigravity]")
			break
		}
		if out := hook.RunAs(client, rest[0], os.Stdin, time.Now()); out != "" {
			fmt.Println(out)
		}
	case "mcp":
		err = mcp.Serve(os.Stdin, os.Stdout)
	case "install":
		err = cmdInstall(args)
	case "import":
		err = cmdImport(args)
	case "remember":
		err = cmdRemember(args)
	case "inbox":
		err = cmdInbox(args)
	case "undo":
		err = cmdUndo(args)
	case "log":
		err = cmdLog(args)
	case "writer":
		err = cmdWriter(args)
	case "capture":
		err = cmdCapture(args)
	case "kb":
		err = cmdKB(args)
	case "version", "--version", "-v":
		fmt.Println("x-mem", mcp.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "不认识的命令 %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "x-mem:", err)
		os.Exit(1)
	}
}

func layers() (config.Layers, error) {
	cwd, _ := os.Getwd()
	return config.Resolve(cwd)
}

func openIndex() (config.Layers, *index.Index, error) {
	l, err := layers()
	if err != nil {
		return l, nil, err
	}
	if len(l.Roots()) == 0 {
		return l, nil, fmt.Errorf("还没有记忆库：先运行 x-mem init")
	}
	ix, err := index.Open(l)
	return l, ix, err
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	personalOnly := fs.Bool("personal", false, "只建个人层")
	fs.Parse(args)
	l, err := layers()
	if err != nil {
		return err
	}
	for _, sub := range []string{"notes", "rules", "inbox"} {
		if err := os.MkdirAll(filepath.Join(l.Personal, sub), 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(l.State, 0o755); err != nil {
		return err
	}
	fmt.Println("个人层：", l.Personal)
	if _, err := os.Stat(filepath.Join(l.Personal, ".git")); os.IsNotExist(err) {
		if _, lookErr := exec.LookPath("git"); lookErr == nil {
			if out, err := exec.Command("git", "-C", l.Personal, "init", "-q").CombinedOutput(); err != nil {
				fmt.Println("  git init 没成功：", strings.TrimSpace(string(out)))
			} else {
				fmt.Println("  已初始化为 git 仓库（推到你自己的私有远端即可多机同步）")
			}
		}
	}
	if *personalOnly {
		return nil
	}
	cwd, _ := os.Getwd()
	root, err := config.RepoRoot(cwd)
	if err != nil {
		root = cwd
		fmt.Println("当前目录不在 git 仓库里，项目层建在当前目录")
	}
	proj := filepath.Join(root, config.DirName)
	for _, sub := range []string{"notes", "rules", "inbox"} {
		if err := os.MkdirAll(filepath.Join(proj, sub), 0o755); err != nil {
			return err
		}
	}
	fmt.Println("项目层：", proj, "（随仓库提交）")
	fmt.Println("\n下一步：x-mem install claude-code")
	return nil
}

func cmdRecall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ExitOnError)
	scope := fs.String("scope", "all", "all、personal 或 project")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)
	q := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if q == "" {
		return fmt.Errorf("用法：x-mem recall 问题")
	}
	_, ix, err := openIndex()
	if err != nil {
		return err
	}
	res := recall.Search(ix, q, recall.Options{Scope: *scope, Limit: 5})
	if *asJSON {
		type hit struct {
			ID, Title, Tier, Source string
			Score, Coverage         float64
		}
		out := struct {
			Tier string
			Hits []hit
		}{Tier: res.Tier.String()}
		for _, h := range res.Hits {
			out.Hits = append(out.Hits, hit{h.Doc.ID, h.Doc.Title, h.Tier.String(), inject.Source(h.Doc), h.Score, h.Coverage})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Print(inject.Answer(res, 3))
	return nil
}

func cmdWhy(args []string) error {
	q := strings.TrimSpace(strings.Join(args, " "))
	if q == "" {
		return fmt.Errorf("用法：x-mem why 问题")
	}
	_, ix, err := openIndex()
	if err != nil {
		return err
	}
	res := recall.Search(ix, q, recall.Options{Limit: 5})
	var units []string
	for _, u := range res.Units {
		units = append(units, u.Text)
	}
	fmt.Printf("问题：%s\n要点（%d 个）：%s\n", q, len(units), strings.Join(units, " "))
	fmt.Printf("检索词：%s\n结论：memory: %s\n\n", strings.Join(textutil.Terms(q), " "), res.Tier)
	if len(res.Hits) == 0 {
		fmt.Println("没有任何生效的笔记和问题有共同的词。")
	}
	for i, h := range res.Hits {
		fmt.Printf("%d. %s（%s）\n", i+1, h.Doc.Title, h.Doc.ID)
		fmt.Printf("   档位 %s：%s\n", h.Tier, h.Why)
		fmt.Printf("   覆盖问题要点 %d/%d（%.0f%%）：%s\n", len(h.Covered), h.Units, h.Coverage*100, strings.Join(h.Covered, " "))
		fmt.Printf("   标题被提到 %d/%d；BM25 %.2f；总分 %.2f\n", h.TitleCovered, len(h.Doc.TitleUnits), h.BM25, h.Score)
	}
	qt := map[string]bool{}
	for _, t := range textutil.Terms(q) {
		qt[t] = true
	}
	for _, d := range ix.Docs {
		if d.Active {
			continue
		}
		for _, t := range textutil.Terms(d.Title + "\n" + d.Note().Body) {
			if qt[t] {
				fmt.Printf("已排除：%s（%s）\n", d.Title, d.Reason)
				break
			}
		}
	}
	return nil
}

func cmdRead(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法：x-mem read <id|文件名|标题>")
	}
	_, ix, err := openIndex()
	if err != nil {
		return err
	}
	d := ix.Lookup(args[0])
	if d == nil {
		return fmt.Errorf("找不到笔记 %q", args[0])
	}
	fmt.Print(mcp.Describe(ix, d))
	return nil
}

func cmdStatus() error {
	l, ix, err := openIndex()
	if err != nil {
		return err
	}
	fmt.Print(mcp.Status(l, ix))
	return nil
}

func cmdReindex() error {
	l, err := layers()
	if err != nil {
		return err
	}
	start := time.Now()
	if err := readDocuments(l, false); err != nil {
		return err
	}
	ix, err := index.Rebuild(l)
	if err != nil {
		return err
	}
	defer ix.Close()
	fmt.Printf("重建完成：%d 条笔记，%d 条生效，用时 %s\n", len(ix.Docs), ix.N, time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdEval(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法：x-mem eval <题集.json>")
		return 2
	}
	f, err := eval.Load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "x-mem:", err)
		return 2
	}
	_, ix, err := openIndex()
	if err != nil {
		fmt.Fprintln(os.Stderr, "x-mem:", err)
		return 2
	}
	if eval.Report(os.Stdout, eval.Run(ix, f, time.Now())) > 0 {
		return 1
	}
	return 0
}

func cmdDoctor() int {
	l, err := layers()
	if err != nil {
		fmt.Println("✗", err)
		return 1
	}
	bad := 0
	fail := func(format string, a ...any) { bad++; fmt.Printf("✗ "+format+"\n", a...) }
	ok := func(format string, a ...any) { fmt.Printf("✓ "+format+"\n", a...) }
	note := func(format string, a ...any) { fmt.Printf("· "+format+"\n", a...) }

	if st, err := os.Stat(l.Personal); err != nil || !st.IsDir() {
		fail("个人层不存在：%s → 运行 x-mem init", l.Personal)
	} else {
		ok("个人层：%s", l.Personal)
	}
	if l.Project != "" {
		ok("项目层：%s", l.Project)
	} else {
		note("当前目录不在带 .x-mem 的项目里（需要时在仓库里运行 x-mem init）")
	}
	if len(l.Roots()) == 0 {
		return 1
	}
	start := time.Now()
	ix, err := index.Open(l)
	if err != nil {
		fail("索引打不开：%v → 运行 x-mem reindex", err)
		return 1
	}
	ok("索引：%d 条笔记，%d 条生效（%s）", len(ix.Docs), ix.N, time.Since(start).Round(time.Millisecond))
	unread := 0
	for _, d := range ix.Docs {
		if d.Layer == "kb" && d.Unread != "" && !d.Waiting {
			unread++
		}
	}
	if ix.Waiting > 0 {
		note("知识库还有 %d 个文档在后台读取（想现在读完：x-mem kb read）", ix.Waiting)
	}
	if unread > 0 {
		note("知识库有 %d 个文档读不出文字（扫描件、加了打开密码等）→ x-mem kb list 看是哪些", unread)
	}
	ids := map[string][]string{}
	for _, d := range ix.Docs {
		if d.Layer == "kb" {
			continue // reference material: its frontmatter is not ours to police
		}
		ids[d.ID] = append(ids[d.ID], d.Rel)
		for _, p := range append(append([]string{}, d.NoteProblems...), d.Problems...) {
			fail("%s：%s", inject.Source(d), p)
		}
		if inject.Suspicious(d) {
			fail("%s：内容像是在给 agent 下指令，不会被注入，请检查", inject.Source(d))
		}
	}
	for id, files := range ids {
		if len(files) > 1 {
			sort.Strings(files)
			fail("id %s 重复：%s", id, strings.Join(files, "、"))
		}
	}
	if lines := recentLogLines(filepath.Join(l.State, "hook.log"), 7*24*time.Hour, 5); len(lines) > 0 {
		fail("钩子最近出过错（state/hook.log）：")
		for _, ln := range lines {
			fmt.Println("    " + ln)
		}
	}
	home, _ := os.UserHomeDir()
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	type client struct {
		name, hooks, mcp, mcpWant, fix string
		events                         []string
	}
	clients := []client{
		{"Claude Code", read(settingsPath()), read(filepath.Join(home, ".claude.json")), "x-mem", "x-mem install claude-code --apply", []string{"start", "prompt", "guard", "stop"}},
		{"Codex", read(filepath.Join(codexDir(), "hooks.json")), read(filepath.Join(codexDir(), "config.toml")), "x-mem", "x-mem install codex --apply（然后在 Codex 里 /hooks 信任）", []string{"start", "prompt", "guard", "stop"}},
		{"Antigravity", read(filepath.Join(geminiConfigDir(), "hooks.json")), read(filepath.Join(geminiConfigDir(), "mcp_config.json")), "x-mem", "x-mem install antigravity --apply", []string{"start", "prompt", "guard", "stop"}},
	}
	installed := map[string]bool{}
	for _, c := range detectClients() {
		installed[map[string]string{"claude-code": "Claude Code", "codex": "Codex", "antigravity": "Antigravity", "claude-desktop": "Claude 桌面版"}[c]] = true
	}
	for _, c := range clients {
		if !installed[c.name] {
			continue
		}
		var missing []string
		for _, ev := range c.events {
			if !strings.Contains(c.hooks, "x-mem hook "+ev) {
				missing = append(missing, ev)
			}
		}
		hasMCP := strings.Contains(c.mcp, c.mcpWant)
		switch {
		case len(missing) == 0 && hasMCP:
			ok("%s：钩子和 MCP 都接好了", c.name)
		case len(missing) == len(c.events) && !hasMCP:
			note("%s 还没接入 → %s", c.name, c.fix)
		default:
			what := []string{}
			if len(missing) > 0 {
				what = append(what, "钩子缺 "+strings.Join(missing, "、"))
			}
			if !hasMCP {
				what = append(what, "MCP 没注册")
			}
			fail("%s：%s → %s", c.name, strings.Join(what, "，"), c.fix)
		}
		if c.name == "Codex" && len(missing) < len(c.events) {
			if untrusted := codexUntrusted(filepath.Join(codexDir(), "hooks.json"), c.hooks, c.mcp); len(untrusted) > 0 {
				fail("Codex：x-mem 的钩子还没被信任（%s），Codex 不会运行它们 → 在 Codex 里输入 /hooks（App 里是 设置 → Hooks）逐个信任", strings.Join(untrusted, "、"))
			}
		}
		if strings.Contains(strings.ToLower(c.hooks+c.mcp), "old-mem") {
			note("%s 的配置里还有 旧记忆库 的条目 → x-mem install all --apply --drop（只拿掉配置条目，先备份）", c.name)
		}
	}
	doctorDesktop(ok, note, fail)
	in := writer.NewInbox(l.State)
	if q := in.List(writer.Queued); len(q) > 0 {
		fail("%d 条提案排队没写进去（%s）→ x-mem writer --once 看原因", len(q), strings.Join(q[0].Problems, "；"))
	}
	if r := in.List(writer.Review); len(r) > 0 {
		note("%d 条提案等你确认 → x-mem inbox", len(r))
	}
	if lines := recentLogLines(filepath.Join(l.State, "writer.log"), 7*24*time.Hour, 3); len(lines) > 0 {
		fail("写入进程最近出过错（state/writer.log）：")
		for _, ln := range lines {
			fmt.Println("    " + ln)
		}
	}
	if st := capture.LoadState(l); st.Enabled {
		ok("会话采集：开")
		if runtime.GOOS == "darwin" && !capture.TimerInstalled() {
			fail("会话采集开着，但定时扫描没装 → x-mem capture on")
		}
	}
	if bad == 0 {
		fmt.Println("\n没有发现问题。")
		return 0
	}
	fmt.Printf("\n发现 %d 个问题。\n", bad)
	return 1
}

// codexUntrusted lists x-mem's hooks that Codex has not been told to trust.
// Codex records trust in config.toml as [hooks.state."<hooks.json>:<event>:
// <group>:<hook>"]; a hook without such an entry never runs. Older Codex
// versions keep no trust records, so nothing is reported for them.
func codexUntrusted(hooksPath, hooksJSON, configTOML string) []string {
	if !strings.Contains(configTOML, "[hooks.state") {
		return nil
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal([]byte(hooksJSON), &file) != nil {
		return nil
	}
	events := []struct{ name, key, label string }{
		{"SessionStart", "session_start", "start"},
		{"UserPromptSubmit", "user_prompt_submit", "prompt"},
		{"PreToolUse", "pre_tool_use", "guard"},
		{"Stop", "stop", "stop"},
	}
	var out []string
	for _, ev := range events {
		for gi, g := range file.Hooks[ev.name] {
			for hi, h := range g.Hooks {
				if !strings.Contains(h.Command, "x-mem hook "+ev.label) {
					continue
				}
				key := fmt.Sprintf("[hooks.state.%q]", fmt.Sprintf("%s:%s:%d:%d", hooksPath, ev.key, gi, hi))
				if !strings.Contains(configTOML, key) {
					out = append(out, ev.name)
				}
			}
		}
	}
	return out
}

func recentLogLines(path string, within time.Duration, max int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	cutoff := time.Now().Add(-within)
	for sc.Scan() {
		line := sc.Text()
		ts, _, _ := strings.Cut(line, " ")
		if t, err := time.Parse(time.RFC3339, ts); err == nil && t.After(cutoff) {
			out = append(out, line)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}
