package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"x-mem/internal/capture"
	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/skills"
	uselog "x-mem/internal/usage"
	"x-mem/internal/writer"
)

// x-mem skills: the skills grown from your experience.
func cmdSkills(args []string) error {
	l, err := layers()
	if err != nil {
		return err
	}
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	now := time.Now()
	switch sub {
	case "list", "ls":
		ix, err := index.Open(l)
		if err != nil {
			return err
		}
		defer ix.Close()
		used := uselog.Since(l, now.AddDate(0, 0, -60))
		var active []*index.Doc
		retired := 0
		for _, d := range ix.Docs {
			if d.Type != "skill" || d.Layer == "kb" {
				continue
			}
			if d.Active {
				active = append(active, d)
			} else {
				retired++
			}
		}
		sort.SliceStable(active, func(i, j int) bool { return times(used, active[i]) > times(used, active[j]) })
		if len(active) == 0 {
			fmt.Println("还没有技能。")
		} else {
			fmt.Printf("技能 %d 个（会话开始时列出前 8 个，用得多的在前）：\n", len(active))
			for _, d := range active {
				fmt.Printf("  %s〔%s〕：%s（60 天里用了 %d 次）\n", d.Title, d.ID, d.When, times(used, d))
			}
		}
		if retired > 0 {
			fmt.Printf("已停用 %d 个。\n", retired)
		}
		st := skills.Load(l)
		fmt.Println()
		fmt.Println("自动整理：" + modeName(l, st))
		if !st.LastRun.IsZero() {
			fmt.Printf("上次：%s，%s\n", st.LastRun.Format("01-02 15:04"), st.Last)
		}
		if n := len(skills.Candidates(l, ix, st, now)); n > 0 {
			fmt.Printf("现在有 %d 组经验够格整理成技能（x-mem skills grow 马上起草，草稿进收件箱）\n", n)
		}
		return nil
	case "grow":
		w := writer.New(l)
		got, err := skills.Grow(l, w, now, true)
		if err != nil {
			return err
		}
		if len(got) == 0 {
			fmt.Println(skills.Load(l).Last)
			return nil
		}
		for _, p := range got {
			fmt.Printf("草稿 %s：%s\n  适用：%s\n", p.ID, p.Title, p.When)
		}
		fmt.Println("都在收件箱里，确认后才生效：x-mem inbox review")
		return nil
	case "on", "off", "auto":
		st := skills.Load(l)
		st.Mode = map[string]string{"on": "on", "off": "off", "auto": ""}[sub]
		if err := skills.Save(l, st); err != nil {
			return err
		}
		fmt.Println("自动整理：" + modeName(l, st))
		return nil
	case "retire":
		if len(args) == 0 {
			return errors.New("用法：x-mem skills retire <id|标题>")
		}
		run, err := writer.New(l).Retire(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("%s（撤回：x-mem undo %s）\n", run.Title, run.ID)
		return nil
	}
	return errors.New("用法：x-mem skills [list|grow|on|off|auto|retire <id>]")
}

func times(used map[string]*uselog.Use, d *index.Doc) int {
	if u := used[d.ID]; u != nil {
		return u.Times
	}
	return 0
}

func modeName(l config.Layers, st skills.State) string {
	switch st.Mode {
	case "on":
		return "开（每天最多一次，草稿进收件箱）"
	case "off":
		return "关"
	}
	if capture.LoadState(l).Enabled {
		return "开（跟着会话采集；x-mem skills off 单独关掉）"
	}
	return "关（跟着会话采集；x-mem skills on 单独打开）"
}

// doctorSkills: how many skills, whether they grow, which nobody uses.
func doctorSkills(l config.Layers, ix *index.Index, ok, note func(string, ...any)) {
	now := time.Now()
	st := skills.Load(l)
	used := uselog.Since(l, now.AddDate(0, 0, -60))
	old := now.AddDate(0, 0, -60).Format("2006-01-02")
	n := 0
	for _, d := range ix.Docs {
		if d.Type != "skill" || !d.Active || d.Layer == "kb" {
			continue
		}
		n++
		if times(used, d) == 0 && d.Updated != "" && d.Updated < old {
			note("技能「%s」60 天没用过 → 不需要了就 x-mem skills retire %s（能撤回）", d.Title, d.ID)
		}
	}
	if n == 0 && !skills.Enabled(l) {
		return
	}
	ok("技能：%d 个；自动整理：%s", n, modeName(l, st))
	if st.Waiting > 0 && st.Last != "" {
		note("%s", st.Last)
	}
}
