package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/extract"
	"x-mem/internal/index"
)

// x-mem kb add|list|remove|read: folders of reference material (an
// Obsidian vault, a docs repo, a folder of Word and PDF files). They are
// searched and cited, but never count as something you decided, never get
// written to, and never go into hooks.
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
		fmt.Printf("知识库已加入：%s\n", dir)
		for _, e := range exclude {
			fmt.Println("  不收：", e)
		}
		if err := readDocuments(l, false); err != nil {
			return err
		}
		ix, err := index.Open(l)
		if err != nil {
			return err
		}
		defer ix.Close()
		printKBCounts(ix, dir)
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
		ix, err := index.Open(l)
		if err != nil {
			return err
		}
		defer ix.Close()
		for _, k := range kbs {
			fmt.Println(k.Path)
			for _, e := range k.Exclude {
				fmt.Println("  不收：", e)
			}
			printKBCounts(ix, k.Path)
		}
		if ix.Waiting > 0 {
			fmt.Printf("\n还有 %d 个文件在后台读取；想现在就读完：x-mem kb read\n", ix.Waiting)
		}
	case "read":
		quiet := len(args) > 1 && args[1] == "--quiet"
		return readDocuments(l, quiet)
	default:
		return errors.New("用法：x-mem kb add <目录> [--exclude 子目录] | list | remove <目录> | read")
	}
	return nil
}

// printKBCounts says what is in one knowledge-base folder.
func printKBCounts(ix *index.Index, dir string) {
	notes, docs, waiting := 0, 0, 0
	var failed []*index.Doc
	for _, d := range ix.Docs {
		if d.Layer != "kb" || !strings.HasPrefix(d.Path, dir+string(filepath.Separator)) {
			continue
		}
		switch {
		case d.TextFile == "":
			notes++
		case d.Waiting:
			waiting++
		case d.Unread != "":
			failed = append(failed, d)
		default:
			docs++
		}
	}
	fmt.Printf("  %d 篇笔记，%d 个文档", notes, docs)
	if waiting > 0 {
		fmt.Printf("，%d 个还在读", waiting)
	}
	if len(failed) > 0 {
		fmt.Printf("，%d 个读不出文字", len(failed))
	}
	fmt.Println()
	for i, d := range failed {
		if i == 5 {
			fmt.Printf("    ……还有 %d 个\n", len(failed)-5)
			break
		}
		fmt.Printf("    %s：%s\n", d.Rel, strings.TrimPrefix(d.Unread, "读不出文字："))
	}
}

// readDocuments reads every document in the knowledge base that has no
// cached text yet (Word, PDF, …). The hooks start it in the background;
// x-mem kb add and kb read run it in the foreground with progress.
func readDocuments(l config.Layers, quiet bool) error {
	lock, ok := extract.TryLock(l.State)
	if !ok {
		if !quiet {
			fmt.Println("后台已经在读了，稍等一会儿再看：x-mem kb list")
		}
		return nil
	}
	defer lock.Release()
	type job struct {
		path        string
		size, mtime int64
	}
	var jobs []job
	for _, root := range index.KBLayers(l).Roots() {
		filepath.WalkDir(root.Dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root.Dir, p)
			if de.IsDir() {
				if p != root.Dir && (config.SkipDir(de.Name()) || root.Excluded(rel)) {
					return filepath.SkipDir
				}
				return nil
			}
			name := de.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".md" || !extract.Supported(ext) || strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") || root.Excluded(rel) {
				return nil
			}
			info, err := de.Info()
			if err != nil {
				return nil
			}
			if _, done := extract.CachedHead(l.State, p, info.Size(), info.ModTime().UnixNano()); !done {
				jobs = append(jobs, job{p, info.Size(), info.ModTime().UnixNano()})
			}
			return nil
		})
	}
	pruned := extract.Prune(l.State)
	if len(jobs) == 0 {
		if pruned > 0 {
			index.InvalidateKB(l)
		}
		return nil
	}
	if !quiet {
		fmt.Printf("读取 %d 个文档……\n", len(jobs))
	}
	start, failed := time.Now(), 0
	for i, j := range jobs {
		text, err := extract.Text(j.path)
		if err != nil {
			failed++
		}
		_ = extract.Store(l.State, j.path, j.size, j.mtime, text, err)
		lock.Touch()
		if !quiet && (i+1)%20 == 0 {
			fmt.Printf("  %d/%d\n", i+1, len(jobs))
		}
	}
	index.InvalidateKB(l)
	if !quiet {
		fmt.Printf("读完 %d 个，用时 %s", len(jobs), time.Since(start).Round(time.Second))
		if failed > 0 {
			fmt.Printf("；%d 个读不出文字（x-mem kb list 看是哪些）", failed)
		}
		fmt.Println()
	}
	return nil
}
