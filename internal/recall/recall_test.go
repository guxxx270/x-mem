package recall_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/eval"
	"x-mem/internal/index"
	"x-mem/internal/recall"
)

// vault writes files (paths relative to the personal layer) into a temp home.
func vault(t *testing.T, files map[string]string) config.Layers {
	t.Helper()
	home := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	for rel, body := range files {
		write(t, filepath.Join(l.Personal, rel), body)
	}
	return l
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func search(t *testing.T, l config.Layers, q string) recall.Result {
	t.Helper()
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	return recall.Search(ix, q, recall.Options{})
}

const rulesPage = `---
id: rules
title: 工作约束
type: preference
---
- 不要移动剪藏原件，复制到正式位置再用。
- 名称要一眼看出用途。
- 时间日志文件名用 DD｜一句话概要。
`

// Replacing one bullet must not retire a whole page of unrelated rules.
func TestLineSupersedeKeepsTheOtherRules(t *testing.T) {
	l := vault(t, map[string]string{
		"notes/rules.md": rulesPage,
		"notes/clip.md": `---
id: clip-v2
title: 剪藏可以移动
type: preference
relations:
  - op: "@supersedes"
    target: rules
    line: "不要移动剪藏原件"
---
剪藏原件可以直接挪到归档文件夹。
`,
	})
	res := search(t, l, "时间日志文件名怎么起")
	if res.Tier != recall.Direct || res.Hits[0].Doc.ID != "rules" {
		t.Fatalf("the untouched rule must still answer: %+v", res)
	}
	ix, _ := index.Open(l)
	for _, line := range ix.Lookup("rules").InForceLines() {
		if line == "- 不要移动剪藏原件，复制到正式位置再用。" {
			t.Fatal("the replaced line must not be in force")
		}
	}
	res = search(t, l, "剪藏原件能移动吗")
	if res.Hits[0].Doc.ID != "clip-v2" {
		t.Fatalf("the new rule must come first, got %s", res.Hits[0].Doc.ID)
	}
}

func TestWholeSupersedeHidesTheOldNote(t *testing.T) {
	l := vault(t, map[string]string{
		"notes/old.md": "---\nid: old\ntitle: 发版日\n---\n我们每周五下午发版。\n",
		"notes/new.md": "---\nid: new\ntitle: 发版日（改）\nrelations:\n  - op: \"@supersedes\"\n    target: old\n---\n发版改到每周二上午，不再周五。\n",
	})
	res := search(t, l, "还是周五发版吗")
	for _, h := range res.Hits {
		if h.Doc.ID == "old" {
			t.Fatal("a superseded note must never be offered")
		}
	}
	if res.Tier != recall.Direct || res.Hits[0].Doc.ID != "new" {
		t.Fatalf("got %+v", res)
	}
}

func TestDanglingAndMisspeltRelationsAreReported(t *testing.T) {
	l := vault(t, map[string]string{
		"notes/a.md": "---\nid: a\nrelations:\n  - op: \"@supersedes\"\n    target: nowhere\n  - op: supersedes\n    target: b\n---\nx\n",
		"notes/b.md": "---\nid: b\n---\ny\n",
	})
	ix, _ := index.Open(l)
	a := ix.Lookup("a")
	if len(a.Problems) != 1 || len(a.NoteProblems) != 1 {
		t.Fatalf("want one link problem and one spelling problem, got %v / %v", a.Problems, a.NoteProblems)
	}
	if !ix.Lookup("b").Active {
		t.Fatal("a misspelt relation must not retire its target")
	}
}

func TestExpiredAndDraftNotesAreOutOfForce(t *testing.T) {
	l := vault(t, map[string]string{
		"notes/exp.md":   "---\nid: exp\ntitle: 临时冻结发版\nvalid_until: 2020-01-01\n---\n冻结发版。\n",
		"notes/draft.md": "---\nid: draft\ntitle: 发版草案\nstatus: draft\n---\n发版草案。\n",
	})
	if res := search(t, l, "冻结发版"); res.Tier != recall.None {
		t.Fatalf("expired and draft notes must not answer: %+v", res)
	}
}

func TestHandEditTakesEffectWithoutReindex(t *testing.T) {
	l := vault(t, map[string]string{"notes/db.md": "---\nid: db\ntitle: 订单服务数据库\n---\n订单服务用 PostgreSQL。\n"})
	if res := search(t, l, "MySQL"); res.Tier != recall.None {
		t.Fatalf("before the edit: %+v", res)
	}
	p := filepath.Join(l.Personal, "notes/db.md")
	write(t, p, "---\nid: db\ntitle: 订单服务数据库\n---\n订单服务改用 MySQL 8。\n")
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	res := search(t, l, "订单服务用 MySQL 吗")
	if res.Tier != recall.Direct || res.Hits[0].Doc.ID != "db" {
		t.Fatalf("after the edit: %+v", res)
	}
}

func TestUnrelatedMessageIsNone(t *testing.T) {
	l := vault(t, map[string]string{"notes/rules.md": rulesPage})
	if res := search(t, l, "楼里的咖啡机又坏了"); res.Tier != recall.None {
		t.Fatalf("got %s", res.Tier)
	}
}

// The phase-one acceptance numbers on the sample vault in testdata/.
func TestRegressionSet(t *testing.T) {
	root, _ := filepath.Abs("../../testdata")
	l := config.Layers{
		Home:     filepath.Join(root, "home"),
		Personal: filepath.Join(root, "home", "personal"),
		State:    t.TempDir(),
		Project:  filepath.Join(root, "proj", ".x-mem"),
	}
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	f, err := eval.Load(filepath.Join(root, "regress.json"))
	if err != nil {
		t.Fatal(err)
	}
	pass := map[string]int{}
	total := map[string]int{}
	for _, o := range eval.Run(ix, f, time.Now()) {
		total[o.Case.Expect]++
		if o.Pass {
			pass[o.Case.Expect]++
		} else {
			t.Logf("miss: %s — %s", o.Case.Q, o.Reason)
		}
	}
	if pass["direct"] < 18 || pass["none"] < 9 || pass["silent"] < total["silent"] {
		t.Fatalf("below the phase-one bar: direct %d/%d, none %d/%d, silent %d/%d",
			pass["direct"], total["direct"], pass["none"], total["none"], pass["silent"], total["silent"])
	}
}
