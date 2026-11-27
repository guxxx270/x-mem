package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/usage"
	"x-mem/internal/writer"
)

func layers(t *testing.T) config.Layers {
	t.Helper()
	home := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	os.MkdirAll(filepath.Join(l.Personal, "notes"), 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", l.Personal}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return l
}

func write(t *testing.T, l config.Layers, name, id, kind, title, body string, extra ...string) {
	t.Helper()
	fm := "---\nid: " + id + "\ntitle: " + title + "\ntype: " + kind + "\n" + strings.Join(extra, "\n")
	if len(extra) > 0 {
		fm += "\n"
	}
	src := fm + "---\n" + body + "\n"
	p := filepath.Join(l.Personal, "notes", name+".md")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two lessons about releasing, one about something else, one fact.
func experiences(t *testing.T, l config.Layers) {
	write(t, l, "a", "L1", "lesson", "发版前先跑 make check", "发版前先跑 make check，上次没跑漏了 lint 错误。")
	write(t, l, "b", "L2", "howto", "发版失败怎么回滚", "发版失败时先 git revert 发版提交，再重跑 make check 和发版脚本。")
	write(t, l, "c", "L3", "lesson", "Redis 连接池别开太大", "Redis 连接池超过 200 会把小实例打满。")
	write(t, l, "d", "F1", "fact", "发版日是周四", "每周四发版。")
}

func candidates(t *testing.T, l config.Layers, st State) []Group {
	t.Helper()
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	return Candidates(l, ix, st, time.Now())
}

func TestExperiencesOnTheSameGroundGroup(t *testing.T) {
	l := layers(t)
	experiences(t, l)
	gs := candidates(t, l, State{})
	if len(gs) != 1 || strings.Join(gs[0].IDs(), ",") != "L1,L2" {
		t.Fatalf("want one group L1,L2, got %+v", ids(gs))
	}
	// Offered once: not again, unless a new note joins.
	st := State{Offered: []Offer{{Sources: []string{"L1", "L2"}}}}
	if gs := candidates(t, l, st); len(gs) != 0 {
		t.Fatalf("offered notes come back: %v", ids(gs))
	}
	write(t, l, "e", "L4", "lesson", "发版脚本要先 make check", "发版脚本第一步就是 make check。")
	if gs := candidates(t, l, st); len(gs) != 1 || len(gs[0].Docs) != 3 {
		t.Fatalf("a new note brings the group back: %v", ids(gs))
	}
}

func TestNotesASkillWasMadeFromAreDone(t *testing.T) {
	l := layers(t)
	experiences(t, l)
	write(t, l, "s", "S1", "skill", "发版", "1. make check\n2. 发版", `when: "发版的时候"`, `sources: ["L1", "L2"]`)
	if gs := candidates(t, l, State{}); len(gs) != 0 {
		t.Fatalf("covered notes seed nothing: %v", ids(gs))
	}
	// Retired skills too.
	os.WriteFile(filepath.Join(l.Personal, "notes", "s.md"), []byte("---\nid: S1\ntitle: 发版\ntype: skill\nstatus: deprecated\nwhen: 发版\nsources: [L1, L2]\n---\n1. a\n2. b\n"), 0o644)
	if gs := candidates(t, l, State{}); len(gs) != 0 {
		t.Fatalf("a retired skill's notes stay used: %v", ids(gs))
	}
}

func TestOneExperienceNeededOftenQualifies(t *testing.T) {
	l := layers(t)
	write(t, l, "c", "L3", "lesson", "Redis 连接池别开太大", "Redis 连接池超过 200 会把小实例打满。")
	if gs := candidates(t, l, State{}); len(gs) != 0 {
		t.Fatal("one note used once is not a skill")
	}
	now := time.Now()
	for d := 0; d < MinDays; d++ {
		usage.Record(l, "recall", now.AddDate(0, 0, -d), "L3")
	}
	if gs := candidates(t, l, State{}); len(gs) != 1 {
		t.Fatalf("needed on %d days: %v", MinDays, ids(gs))
	}
}

func ids(gs []Group) [][]string {
	var out [][]string
	for _, g := range gs {
		out = append(out, g.IDs())
	}
	return out
}

func host(t *testing.T, answer string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "drafter")
	os.WriteFile(p, []byte("#!/bin/sh\ncat > \"$0.prompt\"\nprintf '%s' '"+answer+"'\n"), 0o755)
	return []string{p}
}

func TestGrowFilesADraftForReviewOnly(t *testing.T) {
	l := layers(t)
	experiences(t, l)
	w := writer.New(l)
	w.Settings.Host = host(t, `{"keep":true,"title":"发版","when":"要发版或发版出问题时","steps":["先跑 make check","失败就 git revert 发版提交","重跑 make check 和发版脚本"],"uses":[1,2]}`)
	now := time.Now()
	if got, _ := Grow(l, w, now, false); len(got) != 0 {
		t.Fatal("off by default (capture is off): nothing runs")
	}
	got, err := Grow(l, w, now, true)
	if err != nil || len(got) != 1 {
		t.Fatalf("grow: %v %v", got, err)
	}
	p := got[0]
	if p.Status != writer.Review || p.Kind != "skill" || p.When == "" || strings.Join(p.Sources, ",") != "L1,L2" {
		t.Fatalf("draft must wait for review: %+v", p)
	}
	if !strings.Contains(p.Text, "1. 先跑 make check") || !strings.Contains(p.Text, "整理自") {
		t.Fatalf("steps and sources in the text: %s", p.Text)
	}
	prompt, _ := os.ReadFile(w.Settings.Host[0] + ".prompt")
	if !strings.Contains(string(prompt), "发版前先跑 make check") || strings.Contains(string(prompt), "Redis") {
		t.Fatalf("the host sees the group's notes only: %s", prompt)
	}
	if _, err := os.Stat(filepath.Join(l.Personal, "skills")); err == nil {
		t.Fatal("nothing is written before approval")
	}
	// Not drafted twice.
	if again, _ := Grow(l, w, now.Add(48*time.Hour), true); len(again) != 0 {
		t.Fatal("the same notes are drafted once")
	}
	// Approved: a skill note in skills/, listed with its sources.
	if _, err := w.Approve(p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	ix, _ := index.Open(l)
	defer ix.Close()
	var skill *index.Doc
	for _, d := range ix.Docs {
		if d.Type == "skill" {
			skill = d
		}
	}
	if skill == nil || !skill.Active || !strings.HasPrefix(skill.Rel, "skills/") || skill.When != "要发版或发版出问题时" || len(skill.Sources) != 2 {
		t.Fatalf("approved skill: %+v", skill)
	}
}

func TestOnceADayAndHostCanDecline(t *testing.T) {
	l := layers(t)
	experiences(t, l)
	w := writer.New(l)
	w.Settings.Host = host(t, `{"keep":false}`)
	Save(l, State{Mode: "on"})
	now := time.Now()
	if got, err := Grow(l, w, now, false); err != nil || len(got) != 0 {
		t.Fatalf("declined: %v %v", got, err)
	}
	st := Load(l)
	if len(st.Offered) != 1 || st.LastRun.IsZero() {
		t.Fatalf("a declined group is remembered: %+v", st)
	}
	write(t, l, "e", "L4", "lesson", "发版脚本要先 make check", "发版脚本第一步就是 make check。")
	if got, _ := Grow(l, w, now.Add(time.Hour), false); len(got) != 0 || !Load(l).LastRun.Equal(st.LastRun) {
		t.Fatal("at most once a day")
	}
}

func TestParseHoldsTheShape(t *testing.T) {
	for _, bad := range []string{
		`no json`,
		`{"keep":true,"title":"","when":"x","steps":["a","b"]}`,
		`{"keep":true,"title":"t","when":"x","steps":["only one"]}`,
	} {
		if _, err := parse(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	a, err := parse("好的：\n```json\n" + `{"keep":true,"title":"t","when":"w","steps":["1","2","3","4","5","6","7","8","9","10"]}` + "\n```")
	if err != nil || len(a.Steps) != 8 {
		t.Fatalf("fenced answer, steps capped at 8: %v %v", a, err)
	}
}
