package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"x-mem/internal/config"
	"x-mem/internal/index"
)

// x-mem kb add|list|remove: folders of reference material (an Obsidian
// vault, a docs repo). They are searched and cited, but never count as
// something you decided, never get written to, and never go into hooks.
func cmdKB(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	l, err := layers()
	if err != nil {
		return err
	}
	kbs := config.LoadKB(l.Home)
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("kb add", flag.ExitOnError)
		var exclude multi
		fs.Var(&exclude, "exclude", "不收进来的子目录或文件（相对这个目录），可写多次")
		fs.Parse(reorder(fs, args[1:]))
		if fs.NArg() != 1 {
			return errors.New("用法：x-mem kb add <目录> [--exclude 子目录]…")
		}
		dir, _ := filepath.Abs(fs.Arg(0))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return fmt.Errorf("目录不存在：%s", dir)
		}
		found := false
		for i := range kbs {
			if kbs[i].Path == dir {
				kbs[i].Exclude, found = exclude, true
			}
		}
		if !found {
			kbs = append(kbs, config.KB{Path: dir, Exclude: exclude})
		}
		if err := config.SaveKB(l.Home, kbs); err != nil {
			return err
		}
		l.KB = kbs
		ix, err := index.Open(l)
		if err != nil {
			return err
		}
		defer ix.Close()
		n := 0
		for _, d := range ix.Docs {
			if d.Layer == "kb" && filepath.HasPrefix(d.Path, dir) {
				n++
			}
		}
		fmt.Printf("知识库已加入：%s（%d 页）\n", dir, n)
		for _, e := range exclude {
			fmt.Println("  不收：", e)
		}
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("用法：x-mem kb remove <目录>")
		}
		dir, _ := filepath.Abs(args[1])
		var keep []config.KB
		for _, k := range kbs {
			if k.Path != dir {
				keep = append(keep, k)
			}
		}
		if len(keep) == len(kbs) {
			return fmt.Errorf("知识库里没有 %s", dir)
		}
		if err := config.SaveKB(l.Home, keep); err != nil {
			return err
		}
		fmt.Println("已从知识库移除（文件不动）：", dir)
	case "list", "ls":
		if len(kbs) == 0 {
			fmt.Println("还没有知识库。x-mem kb add <目录> 把资料文件夹（比如 Obsidian 库）加进来。")
			return nil
		}
		for _, k := range kbs {
			fmt.Println(k.Path)
			for _, e := range k.Exclude {
				fmt.Println("  不收：", e)
			}
		}
	default:
		return errors.New("用法：x-mem kb add <目录> [--exclude 子目录] | list | remove <目录>")
	}
	return nil
}
