package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"x-mem/internal/importer"
	"x-mem/internal/index"
)

func cmdImport(args []string) error {
	if len(args) == 0 || args[0] != "old-mem" {
		return fmt.Errorf("用法：x-mem import obsidian [库目录] [--dry-run] [--force]")
	}
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "只列出会做什么，不写文件")
	force := fs.Bool("force", false, "覆盖之前导入过的同名文件")
	fs.Parse(reorder(fs, args[1:]))
	vault := fs.Arg(0)
	if vault == "" {
		vault = defaultVault()
	}
	if vault == "" {
		return fmt.Errorf("没找到 旧记忆库 库：请写上库目录，例如 x-mem import obsidian ~/Documents/知识库")
	}
	plan, err := importer.旧记忆库(vault)
	if err != nil {
		return err
	}
	l, err := layers()
	if err != nil {
		return err
	}
	rules := 0
	for _, n := range plan.Notes {
		if strings.HasPrefix(n.To, "rules/") {
			rules++
		}
	}
	fmt.Printf("旧记忆库 库：%s\n", vault)
	fmt.Printf("要导入 %d 条：规则与偏好 %d 条（拆成一条一事），其他笔记 %d 条\n", len(plan.Notes), rules, len(plan.Notes)-rules)
	if *dry {
		for _, n := range plan.Notes {
			alias := ""
			if len(n.Aliases) > 0 {
				alias = "  别名：" + strings.Join(n.Aliases, "、")
			}
			fmt.Printf("  + %s  ←  %s%s\n", n.To, n.From, alias)
		}
	}
	reasons := map[string][]string{}
	for _, s := range plan.Skipped {
		reasons[s.Reason] = append(reasons[s.Reason], s.From)
	}
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("跳过 %d 页：\n", len(plan.Skipped))
	for _, k := range keys {
		fmt.Printf("  · %d 页：%s\n", len(reasons[k]), k)
		if *dry {
			for _, f := range reasons[k] {
				fmt.Printf("      %s\n", f)
			}
		}
	}
	if *dry {
		fmt.Printf("\n（预演，没有写文件。去掉 --dry-run 会写进 %s）\n", l.Personal)
		return nil
	}
	if _, err := os.Stat(l.Personal); err != nil {
		return fmt.Errorf("个人层还不存在：先运行 x-mem init --personal")
	}
	written, kept, err := plan.Write(l.Personal, *force)
	if err != nil {
		return err
	}
	fmt.Printf("\n已写入 %d 个文件到 %s", written, l.Personal)
	if kept > 0 {
		fmt.Printf("；%d 个之前导入过，没动（要覆盖加 --force）", kept)
	}
	fmt.Println()
	if written > 0 {
		if _, err := os.Stat(filepath.Join(l.Personal, ".git")); err == nil {
			add := exec.Command("git", "-C", l.Personal, "add", "--", "rules/imported", "notes/imported")
			commit := exec.Command("git", "-C", l.Personal, "commit", "-q", "-m", "从 旧记忆库 导入")
			if add.Run() == nil && commit.Run() == nil {
				fmt.Println("已在个人层提交一次 git（撤销：git -C " + l.Personal + " revert HEAD）")
			}
		}
	}
	ix, err := index.Open(l)
	if err != nil {
		return err
	}
	defer ix.Close()
	fmt.Printf("索引：%d 条笔记，%d 条生效。运行 x-mem doctor 检查有没有问题。\n", len(ix.Docs), ix.N)
	return nil
}

// defaultVault finds the vault 旧记忆库's installer recorded.
func defaultVault() string {
	for _, env := range []string{"LEGACY_VAULT", "OBSIDIAN_VAULT"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(cfg, "old-mem", "vault"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
}

// reorder moves flags (and their values) in front of positional arguments,
// so "x-mem import obsidian ~/库 --dry-run" works like the flag-first form.
func reorder(fs *flag.FlagSet, args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, rest...)
}
