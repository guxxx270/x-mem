package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"x-mem/internal/capture"
	"x-mem/internal/skills"
	"x-mem/internal/writer"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// x-mem remember [选项] 内容
func cmdRemember(args []string) error {
	fs := flag.NewFlagSet("remember", flag.ExitOnError)
	kind := fs.String("kind", "fact", "decision / rule / preference / fact / lesson / howto / skill")
	when := fs.String("when", "", "技能：什么情形下用")
	title := fs.String("title", "", "标题（默认取正文第一句）")
	scope := fs.String("scope", "", "personal 或 project（默认：规则、偏好进个人层，其他进当前项目层）")
	sup := fs.String("supersedes", "", "取代哪条笔记（id、文件名或标题）")
	line := fs.String("line", "", "只取代那条笔记里的这一行")
	rel := fs.String("relation", "", "关系：@supersedes（默认）@refines @conflicts_with @deprecated @relates_to")
	block := fs.Bool("block", false, "规则：触发时拦下动作（默认只提醒）")
	conf := fs.Bool("confidential", false, "机密：只进个人层，不进 git")
	var aliases, triggers multi
	fs.Var(&aliases, "alias", "别名，可写多次")
	fs.Var(&triggers, "trigger", "规则的触发条件，如 'Bash(git push*)'，可写多次")
	fs.Parse(reorder(fs, args))
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("用法：x-mem remember [--kind rule] [--supersedes id [--line 原文]] 内容")
	}
	l, err := layers()
	if err != nil {
		return err
	}
	p := &writer.Proposal{
		Source: "cli", Project: l.Project, Kind: *kind, Title: *title, Text: text, Scope: *scope,
		Supersedes: *sup, Line: *line, Relation: *rel, Aliases: aliases, Triggers: triggers, When: *when,
	}
	if *block {
		p.Enforcement = "block"
	} else if len(triggers) > 0 {
		p.Enforcement = "advise"
	}
	if *conf {
		p.Sensitivity = "confidential"
	}
	got, err := writer.New(l).Submit(p)
	if got == nil {
		return err
	}
	fmt.Print(writer.Describe(got, err))
	if got.Status == writer.Rejected {
		os.Exit(1)
	}
	return nil
}

// x-mem inbox [list|show|approve|reject] …
func cmdInbox(args []string) error {
	l, err := layers()
	if err != nil {
		return err
	}
	w := writer.New(l)
	in := writer.NewInbox(l.State)
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		all := len(args) > 0 && args[0] == "--all"
		var ps []*writer.Proposal
		if all {
			ps = in.List()
		} else {
			ps = in.List(writer.Review, writer.Queued)
		}
		if len(ps) == 0 {
			fmt.Println("inbox 是空的。")
			return nil
		}
		for _, p := range ps {
			fmt.Printf("%s  [%s] %s：%s\n", p.ID, statusName(p.Status), p.Kind, p.Title)
			if p.Review != "" {
				fmt.Printf("    %s\n", p.Review)
			}
			if p.Status == writer.Rejected && len(p.Problems) > 0 {
				fmt.Printf("    %s\n", strings.Join(p.Problems, "；"))
			}
		}
		fmt.Println("\n逐条过：x-mem inbox review；全收：approve --all；看详情：show <编号>；写入：approve <编号> [--supersedes id [--line 原文]]；不要：reject <编号>")
	case "show":
		if len(args) != 1 {
			return errors.New("用法：x-mem inbox show <编号>")
		}
		p, err := in.Load(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("提案 %s（%s，来源 %s，%s）\n类型：%s  层：%s\n标题：%s\n\n%s\n", p.ID, statusName(p.Status), p.Source,
			p.Created.Format("2006-01-02 15:04"), p.Kind, p.Scope, p.Title, p.Shown())
		if p.Quote != "" {
			fmt.Printf("\n原话：%s（%s）\n", p.Quote, p.Session)
		}
		if p.Context != "" {
			fmt.Printf("上文：%s\n", p.Context)
		}
		if p.Supersedes != "" {
			fmt.Printf("关系：%s %s %s\n", p.Relation, p.Supersedes, p.Line)
		}
		if p.Review != "" {
			fmt.Printf("待确认：%s\n", p.Review)
		}
		for _, pr := range p.Problems {
			fmt.Printf("问题：%s\n", pr)
		}
	case "approve":
		fs := flag.NewFlagSet("approve", flag.ExitOnError)
		sup := fs.String("supersedes", "", "改成取代这条笔记")
		line := fs.String("line", "", "只取代那条笔记里的这一行")
		all := fs.Bool("all", false, "全收：写入所有等你确认的提案（可能和已有笔记重复的留给你逐条定）")
		fs.Parse(reorder(fs, args))
		if *all {
			printApproveAll(w)
			return nil
		}
		if fs.NArg() == 0 {
			return errors.New("用法：x-mem inbox approve <编号>… [--supersedes id [--line 原文]]，或 approve --all")
		}
		for _, id := range fs.Args() {
			p, err := w.Approve(id, *sup, *line)
			if p != nil {
				fmt.Print(writer.Describe(p, err))
			} else if err != nil {
				fmt.Println(err)
			}
		}
	case "reject":
		if len(args) == 1 && args[0] == "--all" {
			fmt.Printf("已拒绝 %d 条。\n", w.RejectAll())
			return nil
		}
		if len(args) == 0 {
			return errors.New("用法：x-mem inbox reject <编号>…，或 reject --all")
		}
		for _, id := range args {
			if _, err := w.Reject(id, ""); err != nil {
				fmt.Println(err)
				continue
			}
			fmt.Println("已拒绝：", id)
		}
	case "review":
		return reviewInbox(w, os.Stdin)
	default:
		return fmt.Errorf("用法：x-mem inbox [list [--all] | review | show | approve [--all] | reject [--all]]")
	}
	return nil
}

func printApproveAll(w *writer.Writer) {
	written, left, failed := w.ApproveAll()
	for _, p := range written {
		fmt.Printf("已写入：%s\n", p.Title)
	}
	for id, err := range failed {
		fmt.Printf("没写成 %s：%v\n", id, err)
	}
	if len(written)+len(left)+len(failed) == 0 {
		fmt.Println("没有等你确认的提案。")
		return
	}
	if len(left) > 0 {
		fmt.Printf("\n%d 条可能和已有笔记重复，留给你定（x-mem inbox review）：\n", len(left))
		for _, p := range left {
			fmt.Printf("  %s  %s（像 %s）\n", p.ID, p.Title, strings.Join(p.Candidates, "、"))
		}
	}
	if len(written) > 0 {
		fmt.Println("\n写错了可以撤回：x-mem log 找到那次写入，x-mem undo <run>")
	}
}

// reviewInbox walks the proposals waiting for you, one key each.
func reviewInbox(w *writer.Writer, stdin *os.File) error {
	ps := w.Pending()
	if len(ps) == 0 {
		fmt.Println("没有等你确认的提案。")
		return nil
	}
	r := bufio.NewReader(stdin)
	for i, p := range ps {
		fmt.Printf("\n[%d/%d] %s（%s，%s）\n  %s\n", i+1, len(ps), p.Title, p.Kind, p.Created.Format("01-02 15:04"), strings.ReplaceAll(p.Shown(), "\n", "\n  "))
		if p.Quote != "" {
			fmt.Printf("  原话：%s\n", p.Quote)
		}
		if p.Review != "" {
			fmt.Printf("  %s\n", p.Review)
		}
		prompt := "  y 写入 / n 不要 / s 跳过 / q 结束"
		if len(p.Candidates) > 0 {
			prompt = "  y 另记一条 / r 取代 " + p.Candidates[0] + " / n 不要 / s 跳过 / q 结束"
		}
		fmt.Print(prompt + "：")
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "是":
			got, err := w.Approve(p.ID, "", "")
			if got != nil {
				fmt.Print("  " + strings.ReplaceAll(strings.TrimRight(writer.Describe(got, err), "\n"), "\n", "\n  ") + "\n")
			} else if err != nil {
				fmt.Println(" ", err)
			}
		case "r":
			if len(p.Candidates) == 0 {
				fmt.Println("  这条没有可取代的笔记，跳过")
				continue
			}
			got, err := w.Approve(p.ID, p.Candidates[0], "")
			if got != nil {
				fmt.Print("  " + strings.ReplaceAll(strings.TrimRight(writer.Describe(got, err), "\n"), "\n", "\n  ") + "\n")
			}
		case "n", "no", "否":
			if _, err := w.Reject(p.ID, ""); err == nil {
				fmt.Println("  已拒绝")
			}
		case "q", "quit":
			return nil
		default:
			fmt.Println("  跳过")
		}
	}
	return nil
}

func statusName(s string) string {
	switch s {
	case writer.Review:
		return "待确认"
	case writer.Queued:
		return "排队"
	case writer.Applied:
		return "已写入"
	case writer.Rejected:
		return "已拒绝"
	}
	return s
}

// x-mem undo <run>
func cmdUndo(args []string) error {
	if len(args) != 1 {
		return errors.New("用法：x-mem undo <run>（x-mem log 列出最近的写入）")
	}
	l, err := layers()
	if err != nil {
		return err
	}
	r, err := writer.New(l).Undo(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("已撤回 %s：%s\n", r.ID, r.Title)
	return nil
}

// x-mem log
func cmdLog(args []string) error {
	l, err := layers()
	if err != nil {
		return err
	}
	runs := writer.Runs(l.State)
	if len(runs) == 0 {
		fmt.Println("还没有写入记录。")
		return nil
	}
	for i, r := range runs {
		if i >= 30 {
			break
		}
		state := ""
		if r.Undone {
			state = "（已撤回）"
		}
		files := []string{}
		for _, op := range r.Ops {
			files = append(files, op.Kind+" "+shortPath(op.Path))
		}
		fmt.Printf("%s  %s  %s%s\n    %s\n", r.ID, r.At.Format("01-02 15:04"), r.Title, state, strings.Join(files, "；"))
	}
	return nil
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// x-mem writer [--once]
func cmdWriter(args []string) error {
	fs := flag.NewFlagSet("writer", flag.ExitOnError)
	once := fs.Bool("once", false, "处理完手头的就退出（定时任务和 Stop 钩子用）")
	every := fs.Duration("every", 30*time.Minute, "常驻时多久扫一次会话")
	fs.Parse(args)
	l, err := layers()
	if err != nil {
		return err
	}
	w := writer.New(l)
	step := func() {
		if n, err := capture.Run(l, w, time.Now()); err != nil && !errors.Is(err, writer.ErrBusy) {
			logWriter(l.State, "capture: "+err.Error())
		} else if n > 0 {
			fmt.Printf("%s 采集到 %d 条候选，见 x-mem inbox\n", time.Now().Format("15:04"), n)
		}
		if got, err := skills.Grow(l, w, time.Now(), false); err != nil {
			logWriter(l.State, "skills: "+err.Error())
		} else if len(got) > 0 {
			fmt.Printf("%s 起草了 %d 个技能，见 x-mem inbox\n", time.Now().Format("15:04"), len(got))
		}
		if n, err := w.ProcessQueue(); err != nil && !errors.Is(err, writer.ErrBusy) {
			logWriter(l.State, "write: "+err.Error())
			fmt.Println(err)
		} else if n > 0 {
			fmt.Printf("%s 写入 %d 条\n", time.Now().Format("15:04"), n)
		}
	}
	if *once {
		step()
		return nil
	}
	fmt.Println("x-mem 写入进程在运行（Ctrl-C 结束）")
	for {
		step()
		capture.Wait(l, *every)
	}
}

func logWriter(state, msg string) {
	os.MkdirAll(state, 0o700)
	f, err := os.OpenFile(state+"/writer.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
}
