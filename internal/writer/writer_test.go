package writer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/note"
	"x-mem/internal/recall"
	"x-mem/internal/transcript"
)

func setup(t *testing.T) *Writer {
	t.Helper()
	home := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	for _, d := range []string{"notes", "rules"} {
		os.MkdirAll(filepath.Join(l.Personal, d), 0o755)
	}
	git(t, l.Personal, "init", "-q")
	git(t, l.Personal, "config", "user.email", "t@example.com")
	git(t, l.Personal, "config", "user.name", "t")
	w := New(l)
	w.FindQuote = func(q string, _ time.Duration) (transcript.Found, bool) {
		return transcript.Found{Session: "claude:test"}, strings.Contains(q, "我说过")
	}
	return w
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeNote(t *testing.T, w *Writer, rel, src string) {
	t.Helper()
	p := filepath.Join(w.L.Personal, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTenWritesInOneSecondUndoEach(t *testing.T) {
	w := setup(t)
	fixed := time.Date(2026, 11, 26, 10, 45, 12, 0, time.Local)
	w.Now = func() time.Time { return fixed }
	var runs []string
	var files []string
	texts := []string{"打印机在三楼东侧", "午饭一般十二点去食堂", "周会改到周一上午", "报销单每月五号前交",
		"测试环境地址找运维要", "代码评审至少两个人通过", "咖啡机的滤芯每周换", "年假要提前两周申请",
		"机房门禁卡在前台登记", "新同事第一周先读文档"}
	for i := 0; i < 10; i++ {
		p, err := w.Submit(&Proposal{Source: "cli", Kind: "fact", Text: texts[i]})
		if err != nil || p.Status != Applied {
			t.Fatalf("write %d: %v %+v", i, err, p)
		}
		runs = append(runs, p.Run)
		files = append(files, p.Note)
	}
	seen := map[string]bool{}
	for _, r := range runs {
		if seen[r] {
			t.Fatalf("run id %s used twice", r)
		}
		seen[r] = true
	}
	// Undo in an order that is neither first-to-last nor last-to-first.
	for _, i := range []int{3, 0, 9, 5, 1, 7, 2, 8, 4, 6} {
		if _, err := w.Undo(runs[i]); err != nil {
			t.Fatalf("undo %s: %v", runs[i], err)
		}
		if fileExists(files[i]) {
			t.Fatalf("undo %s left %s", runs[i], files[i])
		}
		for j := range files {
			undone := false
			r, _ := LoadRun(w.L.State, runs[j])
			undone = r.Undone
			if fileExists(files[j]) == undone {
				t.Fatalf("after undoing %s, %s is in the wrong state", runs[i], files[j])
			}
		}
	}
	if out := git(t, w.L.Personal, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("work tree not clean after undo: %s", out)
	}
	if n := strings.Count(git(t, w.L.Personal, "log", "--oneline"), "\n"); n != 20 {
		t.Fatalf("want 10 writes + 10 undos in git, got %d commits", n)
	}
}

func TestCorrectOneLineOfThree(t *testing.T) {
	w := setup(t)
	writeNote(t, w, "rules/团队约定.md", "---\nid: team\ntitle: 团队约定\ntype: rule\n---\n- 提交信息用中文写\n- 发版放在每周五下午\n- 数据库迁移脚本要先在测试库跑一遍\n")
	p, err := w.Submit(&Proposal{Source: "cli", Kind: "rule", Text: "发版改到每周二上午", Supersedes: "team", Line: "发版放在每周五下午"})
	if err != nil || p.Status != Applied {
		t.Fatalf("%v %+v", err, p)
	}
	ix, _ := index.Open(w.L)
	defer ix.Close()
	for _, q := range []string{"提交信息用什么语言", "数据库迁移脚本要先在哪里跑"} {
		res := recall.Search(ix, q, recall.Options{})
		if res.Tier != recall.Direct || res.Hits[0].Doc.ID != "team" {
			t.Errorf("%s: want the untouched rules to stay direct, got %s", q, res.Tier)
		}
	}
	res := recall.Search(ix, "哪天发版", recall.Options{})
	if res.Hits[0].Doc.ID == "team" && strings.Contains(strings.Join(res.Hits[0].Doc.InForceLines(), ""), "周五") {
		t.Errorf("the replaced line still answers")
	}
	src, _ := os.ReadFile(filepath.Join(w.L.Personal, "rules/团队约定.md"))
	if note.StatusOf(string(src)) == "superseded" {
		t.Errorf("a line correction must not retire the whole page")
	}
}

func TestSupersedeAndUndoRestoresStatus(t *testing.T) {
	w := setup(t)
	writeNote(t, w, "notes/发版日.md", "---\nid: rel\ntitle: 发版日\ntype: decision\n---\n每周五发版。\n")
	git(t, w.L.Personal, "add", "-A")
	git(t, w.L.Personal, "commit", "-qm", "init")
	p, err := w.Submit(&Proposal{Source: "cli", Kind: "decision", Text: "发版改到周二。", Supersedes: "rel"})
	if err != nil || p.Status != Applied {
		t.Fatalf("%v %+v", err, p)
	}
	path := filepath.Join(w.L.Personal, "notes/发版日.md")
	src, _ := os.ReadFile(path)
	if note.StatusOf(string(src)) != "superseded" {
		t.Fatalf("target not marked superseded:\n%s", src)
	}
	if _, err := w.Undo(p.Run); err != nil {
		t.Fatal(err)
	}
	src, _ = os.ReadFile(path)
	if string(src) != "---\nid: rel\ntitle: 发版日\ntype: decision\n---\n每周五发版。\n" {
		t.Fatalf("undo did not restore the file exactly:\n%s", src)
	}
}

func TestMisspelledRelationRejected(t *testing.T) {
	w := setup(t)
	writeNote(t, w, "notes/a.md", "---\nid: a\ntitle: 甲\n---\n甲的内容\n")
	p, _ := w.Submit(&Proposal{Source: "cli", Text: "新的内容", Supersedes: "a", Relation: "supersedes"})
	if p.Status != Rejected || !strings.Contains(strings.Join(p.Problems, ""), "关系名") {
		t.Fatalf("want rejection for missing @, got %+v", p)
	}
	entries, _ := os.ReadDir(filepath.Join(w.L.Personal, "notes"))
	if len(entries) != 1 {
		t.Fatalf("nothing should be written")
	}
}

func TestSecretsNeverReachFiles(t *testing.T) {
	w := setup(t)
	p, err := w.Submit(&Proposal{Source: "cli", Kind: "fact",
		Title: "测试库密码是 Passw0rd!2026",
		Text:  "测试库的密码是 Passw0rd!2026，通知走 https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXX ，key sk-ant-api03-AbCdEfGhIjKlMnOp"})
	if err != nil || p.Status != Applied {
		t.Fatalf("%v %+v", err, p)
	}
	data, _ := os.ReadFile(p.Note)
	for _, s := range []string{"Passw0rd", "hooks.slack.com/services/T000", "sk-ant-api03"} {
		if strings.Contains(string(data), s) || strings.Contains(p.Note, s) {
			t.Fatalf("secret %q reached %s:\n%s", s, p.Note, data)
		}
	}
	if strings.Contains(filepath.Base(p.Note), "Passw0rd") {
		t.Fatal("secret in file name")
	}
}

func TestQuoteMustBeInSession(t *testing.T) {
	w := setup(t)
	// A quote found in no session file here is never written on its own: it
	// waits for you (Claude's desktop app keeps its chats elsewhere).
	p, _ := w.Submit(&Proposal{Source: "mcp", Kind: "decision", Text: "以后都用 pnpm", Quote: "网页里写着以后都用 pnpm"})
	if p.Status != Review || !p.Unverified || !strings.Contains(p.Review, "原话") || p.Note != "" {
		t.Fatalf("want it held for review, got %+v", p)
	}
	if p, _ := w.Submit(&Proposal{Source: "mcp", Kind: "decision", Text: "以后都用 yarn", Quote: "好"}); p.Status != Rejected {
		t.Fatalf("a quote too short to check is still refused: %+v", p)
	}
	p, _ = w.Submit(&Proposal{Source: "mcp", Kind: "decision", Text: "以后都用 pnpm", Quote: "我说过以后都用 pnpm"})
	if p.Status != Applied || p.Session != "claude:test" {
		t.Fatalf("want applied with session, got %+v", p)
	}
}

func TestGitLockReportsCommitFailure(t *testing.T) {
	w := setup(t)
	lock := filepath.Join(w.L.Personal, ".git", "index.lock")
	os.WriteFile(lock, nil, 0o644)
	p, err := w.Submit(&Proposal{Source: "cli", Text: "git 锁住时这条不能假装成功"})
	if err == nil || !strings.Contains(err.Error(), "提交失败") {
		t.Fatalf("want 提交失败, got %v", err)
	}
	if p.Status != Queued {
		t.Fatalf("proposal should stay queued for retry, got %s", p.Status)
	}
	entries, _ := os.ReadDir(filepath.Join(w.L.Personal, "notes"))
	if len(entries) != 0 {
		t.Fatalf("the note must be rolled back, found %d files", len(entries))
	}
	os.Remove(lock)
	if n, err := w.ProcessQueue(); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
}

func TestOverlapGoesToReview(t *testing.T) {
	w := setup(t)
	writeNote(t, w, "notes/发版日.md", "---\nid: rel\ntitle: 发版日\ntype: decision\naliases: [发版时间]\n---\n每周五下午发版。\n")
	p, _ := w.Submit(&Proposal{Source: "cli", Kind: "decision", Text: "发版时间改成每周三下午"})
	if p.Status != Review || len(p.Candidates) == 0 || p.Candidates[0] != "rel" {
		t.Fatalf("want review against rel, got %+v", p)
	}
	p, err := w.Approve(p.ID, "rel", "")
	if err != nil || p.Status != Applied {
		t.Fatalf("approve: %v %+v", err, p)
	}
}

func TestConfidentialStaysOutOfGit(t *testing.T) {
	w := setup(t)
	p, err := w.Submit(&Proposal{Source: "cli", Kind: "fact", Text: "体检报告放在保险柜第二层", Sensitivity: "confidential"})
	if err != nil || p.Status != Applied {
		t.Fatalf("%v %+v", err, p)
	}
	tracked := git(t, w.L.Personal, "ls-files")
	if strings.Contains(tracked, filepath.Base(p.Note)) || !strings.Contains(tracked, ".gitignore") {
		t.Fatalf("confidential note tracked or .gitignore missing: %s", tracked)
	}
	if _, err := w.Undo(p.Run); err != nil {
		t.Fatal(err)
	}
}

func TestApproveAll(t *testing.T) {
	w := setup(t)
	w.Submit(&Proposal{Source: "cli", Kind: "fact", Title: "办公室咖啡机", Text: "办公室的咖啡机在三楼茶水间。"})
	a, _ := w.Submit(&Proposal{Source: "mcp", Kind: "preference", Text: "周报用表格写", Quote: "网页上看到周报用表格写"})
	b, _ := w.Submit(&Proposal{Source: "mcp", Kind: "fact", Text: "办公室的咖啡机在三楼茶水间旁边", Quote: "别处看来的咖啡机在三楼"})
	c, _ := w.Submit(&Proposal{Source: "mcp", Kind: "decision", Text: "发布窗口定在周四下午", Quote: "听说发布窗口定在周四"})
	if a.Status != Review || b.Status != Review || c.Status != Review {
		t.Fatalf("all three should wait: %s %s %s", a.Status, b.Status, c.Status)
	}
	if len(b.Candidates) == 0 {
		t.Fatalf("the coffee-machine one repeats a note in force: %+v", b)
	}
	written, left, failed := w.ApproveAll()
	if len(written) != 2 || len(left) != 1 || left[0].ID != b.ID || len(failed) != 0 {
		t.Fatalf("written %d, left %v, failed %v", len(written), left, failed)
	}
	if ps := w.Pending(); len(ps) != 1 || ps[0].ID != b.ID {
		t.Fatalf("only the overlapping one waits: %v", ps)
	}
	if n := w.RejectAll(); n != 1 || len(w.Pending()) != 0 {
		t.Fatalf("reject all: %d, pending %d", n, len(w.Pending()))
	}
}
