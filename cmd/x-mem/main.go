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
	"sort"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/eval"
	"x-mem/internal/hook"
	"x-mem/internal/index"
	"x-mem/internal/inject"
	"x-mem/internal/mcp"
	"x-mem/internal/recall"
	"x-mem/internal/textutil"
)

const usage = `x-mem：给编程 agent 用的本地记忆（第一期：只读）

用法：
  x-mem init [--personal]        建个人层；在当前仓库里建项目层（--personal 只建个人层）
  x-mem recall [--scope S] 问题   查记忆，第一行是 direct / related / none
  x-mem why 问题                  解释为什么是这个结果
  x-mem read <id|文件名|标题>     看一条笔记的全文和关系
  x-mem status                    库和索引的状态
  x-mem doctor                    体检，给出修复办法
  x-mem reindex                   删掉索引重建（平时不需要）
  x-mem eval <题集.json>          跑回归题集
  x-mem hook start|prompt         Claude Code 钩子（从标准输入读事件）
  x-mem mcp                       以 MCP 服务运行（stdio）
  x-mem install claude-code       打印接入 Claude Code 要加的配置
  x-mem version

环境变量：X_MEM_HOME 改个人层位置（默认 ~/.x-mem），X_MEM_PROJECT 指定项目层。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
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
		if len(args) != 1 {
			err = fmt.Errorf("用法：x-mem hook start|prompt")
			break
		}
		if out := hook.Run(args[0], os.Stdin, time.Now()); out != "" {
			fmt.Println(out)
		}
	case "mcp":
		err = mcp.Serve(os.Stdin, os.Stdout)
	case "install":
		err = cmdInstall(args)
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
	ix, err := index.Rebuild(l)
	if err != nil {
		return err
	}
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
	ids := map[string][]string{}
	for _, d := range ix.Docs {
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
	if data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); err == nil && strings.Contains(string(data), "x-mem hook") {
		ok("Claude Code 钩子已配置")
	} else {
		note("Claude Code 钩子还没配置 → x-mem install claude-code")
	}
	if data, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil && strings.Contains(string(data), "x-mem") {
		ok("Claude Code 里注册了 x-mem MCP")
	} else {
		note("还没在 Claude Code 里注册 MCP → x-mem install claude-code")
	}
	if bad == 0 {
		fmt.Println("\n没有发现问题。")
		return 0
	}
	fmt.Printf("\n发现 %d 个问题。\n", bad)
	return 1
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

func cmdInstall(args []string) error {
	if len(args) != 1 || args[0] != "claude-code" {
		return fmt.Errorf("用法：x-mem install claude-code")
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "x-mem"
	}
	exe, _ = filepath.EvalSymlinks(exe)
	hookCmd := func(ev string) map[string]any {
		return map[string]any{"hooks": []map[string]any{{"type": "command", "command": exe + " hook " + ev, "timeout": 5}}}
	}
	snippet := map[string]any{"hooks": map[string]any{
		"SessionStart":     []any{hookCmd("start")},
		"UserPromptSubmit": []any{hookCmd("prompt")},
	}}
	data, _ := json.MarshalIndent(snippet, "", "  ")
	fmt.Println("1. 把下面的 hooks 合并进 ~/.claude/settings.json（已有 hooks 时只加这两项，别覆盖）：")
	fmt.Println()
	fmt.Println(string(data))
	fmt.Println()
	fmt.Println("2. 注册 MCP 服务：")
	fmt.Println()
	fmt.Printf("   claude mcp add --scope user x-mem -- %s mcp\n\n", exe)
	fmt.Println("3. 重开 Claude Code，运行 x-mem doctor 确认。")
	fmt.Println("\nx-mem 不会替你改这些文件；第二期会加 --apply（先备份再合并）。")
	return nil
}
