package writer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/guard"
	"x-mem/internal/index"
	"x-mem/internal/note"
	"x-mem/internal/recall"
	"x-mem/internal/redact"
	"x-mem/internal/textutil"
	"x-mem/internal/transcript"
)

// MaxRunes is the longest note body: past it, a note is two notes.
const MaxRunes = 2000

// Kinds a proposal may have.
var Kinds = []string{"decision", "rule", "preference", "fact", "lesson", "howto", "project", "convention", "policy"}

// Settings are the user's choices, from ~/.x-mem/config.json.
type Settings struct {
	// CommitProject lets the writer commit in project repositories too. Off
	// by default: notes there are left as changes for you to commit with
	// your code.
	CommitProject bool `json:"commit_project"`
	// AutoApplyCapture writes notes distilled from sessions without asking.
	// Off by default: they wait in the inbox.
	AutoApplyCapture bool `json:"auto_apply_capture"`
	// Host is the command that runs a distillation task, prompt on stdin.
	Host []string `json:"host,omitempty"`
	// QuoteWindow is how far back (hours) a quote is looked for.
	QuoteWindowHours int `json:"quote_window_hours,omitempty"`
}

// LoadSettings reads config.json in the x-mem home; missing means defaults.
func LoadSettings(home string) Settings {
	var s Settings
	if data, err := os.ReadFile(filepath.Join(home, "config.json")); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.QuoteWindowHours <= 0 {
		s.QuoteWindowHours = 72
	}
	return s
}

// Writer applies proposals to the notes.
type Writer struct {
	L        config.Layers // Home, Personal and State; Project comes from each proposal
	Settings Settings
	Now      func() time.Time
	// FindQuote looks a quote up in session files (replaceable in tests).
	FindQuote func(quote string, within time.Duration) (transcript.Found, bool)
}

// New makes a writer for the x-mem home of these layers.
func New(l config.Layers) *Writer {
	return &Writer{L: l, Settings: LoadSettings(l.Home), Now: time.Now, FindQuote: retryFind}
}

// retryFind gives Claude Code a moment to flush the session file: the
// message that asked for the note may not be on disk yet.
func retryFind(q string, within time.Duration) (transcript.Found, bool) {
	for i := 0; i < 3; i++ {
		if f, ok := transcript.FindQuote(q, within); ok {
			return f, true
		}
		time.Sleep(700 * time.Millisecond)
	}
	return transcript.Found{}, false
}

func (w *Writer) inbox() Inbox { return NewInbox(w.L.State) }

func (w *Writer) layers(p *Proposal) config.Layers {
	l := w.L
	l.Project = p.Project
	return l
}

// Submit files a proposal: checks it, and when it passes and needs no
// decision from you, writes it straight away.
func (w *Writer) Submit(p *Proposal) (*Proposal, error) {
	now := w.Now()
	p.ID = "p-" + RunID(now)
	p.Created = now
	if p.Source == "" {
		p.Source = "cli"
	}
	// An agent's MCP server may run outside the project: the session the
	// quote came from says which folder the user was working in.
	if p.Source == "mcp" && len([]rune(transcript.Norm(p.Quote))) >= 4 {
		if f, ok := w.FindQuote(p.Quote, time.Duration(w.Settings.QuoteWindowHours)*time.Hour); ok {
			p.Session, p.QuoteAt, p.quoteFound = f.Session, f.Time, true
			if p.Project == "" && f.Cwd != "" {
				p.Project = config.FindProject(f.Cwd, w.L.Home)
			}
		}
	}
	w.normalize(p)
	ix, err := index.Open(w.layers(p))
	if err != nil {
		return nil, err
	}
	p.Problems = w.check(p, ix, true)
	ix.Close()
	switch {
	case len(p.Problems) > 0:
		p.Status = Rejected
	case p.Review != "":
		p.Status = Review
	default:
		p.Status = Queued
	}
	if err := w.inbox().Save(p); err != nil {
		return nil, err
	}
	if p.Status != Queued {
		return p, nil
	}
	lock, err := Acquire(w.L.State, 5*time.Second)
	if err != nil {
		return p, nil // the next writer run picks it up
	}
	defer lock.Release()
	err = w.applyLocked(p)
	return p, err
}

// normalize fills defaults and replaces secrets before anything is stored.
func (w *Writer) normalize(p *Proposal) {
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	if p.Kind == "" {
		p.Kind = "fact"
	}
	p.Text = strings.TrimSpace(p.Text)
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		p.Title = textutil.TitleFrom(p.Text, 24)
	}
	p.Sensitivity = strings.ToLower(strings.TrimSpace(p.Sensitivity))
	p.Enforcement = strings.ToLower(strings.TrimSpace(p.Enforcement))
	p.Relation = strings.TrimSpace(p.Relation)
	if p.Supersedes != "" && p.Relation == "" {
		p.Relation = "@supersedes"
	}
	if p.Sensitivity == "confidential" {
		p.Scope = "personal"
	}
	if p.Scope == "" {
		if isRuleKind(p.Kind) || p.Project == "" {
			p.Scope = "personal"
		} else {
			p.Scope = "project"
		}
	}
	if p.Scope == "personal" {
		p.Project = ""
	}
	seen := map[string]bool{}
	clean := func(s string) string {
		out, found := redact.Text(s)
		for _, f := range found {
			if !seen[f.Kind] {
				seen[f.Kind] = true
				p.Redacted = append(p.Redacted, f.Kind)
			}
		}
		return out
	}
	p.Title = clean(p.Title)
	p.Text = clean(p.Text)
	p.Context = clean(p.Context)
	for i := range p.Aliases {
		p.Aliases[i] = clean(p.Aliases[i])
	}
	// The quote is checked against the session file before it is redacted.
}

func isRuleKind(k string) bool {
	return k == "rule" || k == "preference" || k == "convention" || k == "policy"
}

// check returns what is wrong with a proposal. It also fills p.Review when
// the proposal is fine but you should decide (it overlaps a note already in
// force, or it came from session capture).
func (w *Writer) check(p *Proposal, ix *index.Index, verifyQuote bool) []string {
	var problems []string
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	if p.Text == "" {
		add("正文是空的")
	}
	if n := len([]rune(p.Text)); n > MaxRunes {
		add("正文 %d 字，超过 %d 字上限：一条笔记只写一件事，请拆开", n, MaxRunes)
	}
	if !contains(Kinds, p.Kind) {
		add("类型 %q 不认识，只能用 %s", p.Kind, strings.Join(Kinds, "、"))
	}
	if p.Scope != "personal" && p.Scope != "project" {
		add("scope %q 只能是 personal 或 project", p.Scope)
	}
	if p.Scope == "project" && p.Project == "" {
		add("当前目录不在带 .x-mem 的项目里，不能写进项目层（在仓库里运行 x-mem init，或改用 personal）")
	}
	if p.Enforcement != "" && p.Enforcement != "advise" && p.Enforcement != "block" {
		add("enforcement %q 只能是 advise 或 block", p.Enforcement)
	}
	if len(p.Triggers) > 0 && !isRuleKind(p.Kind) {
		add("只有规则（rule / preference）能带触发条件")
	}
	for _, t := range p.Triggers {
		if _, _, err := guard.ParseTrigger(t); err != nil {
			add("触发条件 %q 写法不对：%v", t, err)
		}
	}
	if note.Suspicious(p.Title + "\n" + p.Text) {
		add("内容像是在给 agent 下指令（例如“忽略之前的指令”），不写入")
	}

	var target *index.Doc
	if p.Relation != "" {
		if !contains(note.KnownOps, p.Relation) {
			add("关系名 %q 不认识，只能用 %s（注意前面的 @）", p.Relation, strings.Join(note.KnownOps, "、"))
		} else if p.Supersedes == "" {
			add("写了关系 %s，但没写它指向哪条笔记", p.Relation)
		}
	}
	if p.Supersedes != "" && len(problems) == 0 {
		target = ix.Lookup(p.Supersedes)
		switch {
		case target == nil:
			add("要%s的笔记 %q 找不到（用 id、文件名或标题）", opName(p.Relation), p.Supersedes)
		case target.Layer == "kb":
			add("「%s」是知识库里的参考资料，不是记忆，不能被%s", target.Title, opName(p.Relation))
		case target.SupersededBy != "":
			add("「%s」已经被 %s 取代了，应该针对现在生效的那条", target.Title, target.SupersededBy)
		case p.Line != "" && p.Relation != "@supersedes" && p.Relation != "@deprecated":
			add("只有 @supersedes / @deprecated 能指定 line")
		case p.Line != "" && !hasLine(target, p.Line):
			add("「%s」里没有这一行：%s", target.Title, p.Line)
		case target.Layer == "project" && p.Scope == "personal" && p.Relation == "@supersedes":
			add("个人层的笔记不能取代项目层的「%s」：项目的决定要在项目层里改", target.Title)
		}
	}

	if verifyQuote {
		switch p.Source {
		case "mcp":
			if len([]rune(transcript.Norm(p.Quote))) < 4 {
				add("缺少你的原话（quote）：自动写入必须带上你在会话里说过的话")
			} else if p.quoteFound {
				// found in Submit
			} else if f, ok := w.FindQuote(p.Quote, time.Duration(w.Settings.QuoteWindowHours)*time.Hour); !ok {
				add("原话在最近的会话记录里找不到，不写入：%s", cut(p.Quote, 40))
			} else {
				p.Session, p.QuoteAt = f.Session, f.Time
			}
		}
	}
	p.Quote, _ = redact.Text(p.Quote)
	if len(problems) > 0 {
		return problems
	}

	// Would this say again, or contradict, something already in force?
	p.Review, p.Candidates = "", nil
	if p.Source == "capture" && !w.Settings.AutoApplyCapture {
		p.Review = "从会话里自动采集的，确认后才写入"
	}
	res := recall.Search(ix, p.Title+"\n"+p.Text, recall.Options{Limit: 3, Now: w.Now(), NoRelated: true})
	for _, h := range res.Hits {
		if h.Tier != recall.Direct || h.Doc == target {
			continue
		}
		p.Candidates = append(p.Candidates, h.Doc.ID)
	}
	if len(p.Candidates) > 0 && p.Relation == "" {
		msg := "和已有的笔记说的可能是同一件事：" + strings.Join(p.Candidates, "、") +
			"。要取代它就 approve --supersedes <id>，另记一条就直接 approve"
		if p.Review == "" {
			p.Review = msg
		} else {
			p.Review += "；" + msg
		}
	}
	return nil
}

func opName(op string) string {
	switch op {
	case "@refines":
		return "细化"
	case "@conflicts_with":
		return "标记冲突"
	case "@deprecated":
		return "作废"
	case "@relates_to":
		return "关联"
	}
	return "取代"
}

func hasLine(d *index.Doc, line string) bool {
	for _, l := range d.Note().BodyLines() {
		if strings.Contains(textutil.Normalize(l), textutil.Normalize(strings.TrimSpace(line))) {
			return true
		}
	}
	return false
}

// Approve writes a proposal that was waiting for you, optionally turning it
// into a correction of an existing note first.
func (w *Writer) Approve(id, supersedes, line string) (*Proposal, error) {
	in := w.inbox()
	p, err := in.Load(id)
	if err != nil {
		return nil, err
	}
	if p.Status == Applied {
		return p, fmt.Errorf("提案 %s 已经写入过了（run %s）", p.ID, p.Run)
	}
	if supersedes != "" {
		p.Supersedes, p.Relation, p.Line = supersedes, "@supersedes", line
	}
	lock, err := Acquire(w.L.State, 10*time.Second)
	if err != nil {
		return p, err
	}
	defer lock.Release()
	ix, err := index.Open(w.layers(p))
	if err != nil {
		return p, err
	}
	// Your approval stands in for the quote check and the overlap review.
	p.Problems = w.check(p, ix, false)
	ix.Close()
	p.Review, p.Candidates = "", nil
	if len(p.Problems) > 0 {
		p.Status = Rejected
		in.Save(p)
		return p, errors.New(strings.Join(p.Problems, "；"))
	}
	p.Status = Queued
	return p, w.applyLocked(p)
}

// Reject drops a proposal without writing it.
func (w *Writer) Reject(id, reason string) (*Proposal, error) {
	in := w.inbox()
	p, err := in.Load(id)
	if err != nil {
		return nil, err
	}
	if p.Status == Applied {
		return p, fmt.Errorf("提案 %s 已经写入了；要撤回用 x-mem undo %s", p.ID, p.Run)
	}
	p.Status = Rejected
	if reason == "" {
		reason = "你拒绝了"
	}
	p.Problems = append(p.Problems, reason)
	return p, in.Save(p)
}

// ProcessQueue writes every queued proposal. It returns how many it wrote;
// ErrBusy means another writer is at it.
func (w *Writer) ProcessQueue() (int, error) {
	queued := w.inbox().List(Queued)
	if len(queued) == 0 {
		return 0, nil
	}
	lock, err := Acquire(w.L.State, 0)
	if err != nil {
		return 0, err
	}
	defer lock.Release()
	n := 0
	var errs []string
	for _, p := range queued {
		if err := w.applyLocked(p); err != nil {
			errs = append(errs, p.ID+"："+err.Error())
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, errors.New(strings.Join(errs, "\n"))
	}
	return n, nil
}

// applyLocked writes one proposal. The caller holds the lock.
func (w *Writer) applyLocked(p *Proposal) error {
	in := w.inbox()
	l := w.layers(p)
	ix, err := index.Open(l)
	if err != nil {
		return err
	}
	defer ix.Close()
	if probs := w.check(p, ix, false); len(probs) > 0 {
		p.Status, p.Problems = Rejected, probs
		in.Save(p)
		return errors.New(strings.Join(probs, "；"))
	}
	p.Review, p.Candidates = "", nil

	root := l.Personal
	if p.Scope == "project" {
		root = l.Project
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("记忆库目录不存在：%s（先运行 x-mem init）", root)
	}
	now := w.Now()
	id := NoteID(now)
	for ix.Lookup(id) != nil {
		id = NoteID(now)
	}
	sub := "notes"
	if isRuleKind(p.Kind) {
		sub = "rules"
	}
	name := fileTitle(p.Title) + "-" + id[len(id)-5:] + ".md"
	path := filepath.Join(root, sub, name)

	d := note.Draft{
		ID: id, Title: p.Title, Type: p.Kind, Aliases: p.Aliases,
		ValidFrom: now.Format("2006-01-02"), Quote: p.Quote, Session: p.Session,
		Triggers: p.Triggers, Enforcement: p.Enforcement, Sensitivity: p.Sensitivity,
		Body: p.Text,
	}
	if p.QuoteAt != "" {
		d.At = p.QuoteAt
	} else if p.Source != "cli" {
		d.At = now.Format(time.RFC3339)
	}
	var target *index.Doc
	if p.Supersedes != "" {
		target = ix.Lookup(p.Supersedes)
		d.Relations = []note.Relation{{Op: p.Relation, Target: target.ID, Line: p.Line}}
	}

	run := &Run{ID: RunID(now), Proposal: p.ID, Title: p.Title, At: now}
	for fileExists(filepath.Join(w.L.State, "runs", run.ID+".json")) {
		run.ID = RunID(now)
	}
	tx := &tx{}
	fail := func(err error) error {
		tx.rollback()
		p.Problems = []string{err.Error()}
		in.Save(p) // stays queued: it is retried on the next writer run
		return err
	}
	if err := tx.create(path, d.Render()); err != nil {
		return fail(err)
	}
	if target != nil && p.Line == "" && (p.Relation == "@supersedes" || p.Relation == "@deprecated") {
		status := "superseded"
		if p.Relation == "@deprecated" {
			status = "deprecated"
		}
		if err := tx.setStatus(target.Path, status); err != nil {
			return fail(err)
		}
	}
	if p.Sensitivity == "confidential" {
		rel, _ := filepath.Rel(l.Personal, path)
		if err := tx.ignore(filepath.Join(l.Personal, ".gitignore"), "/"+filepath.ToSlash(rel)); err != nil {
			return fail(err)
		}
	}
	run.Ops = tx.ops

	commitLayer := p.Scope == "personal" || w.Settings.CommitProject
	if gitRoot := repoRoot(root); gitRoot != "" && commitLayer {
		run.Root = gitRoot
		paths := tx.paths(p.Sensitivity == "confidential", path)
		if err := commit(gitRoot, paths, fmt.Sprintf("x-mem: %s（run %s）", p.Title, run.ID)); err != nil {
			return fail(fmt.Errorf("提交失败：%v", err))
		}
		run.Committed = true
	}
	if err := saveRun(w.L.State, run); err != nil {
		return fail(err)
	}
	p.Status, p.Run, p.Problems, p.Note = Applied, run.ID, nil, path
	return in.Save(p)
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]+`)

// fileTitle is the title made safe for a file name. File names come from
// the title only, never the body (agentcairn put a password in one).
func fileTitle(title string) string {
	s := strings.TrimSpace(unsafeName.ReplaceAllString(title, "-"))
	s = strings.Trim(s, ". ")
	if rs := []rune(s); len(rs) > 40 {
		s = string(rs[:40])
	}
	if s == "" {
		s = "笔记"
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func cut(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "……"
}

// repoRoot is the git work tree that contains dir, or "".
func repoRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// commit records exactly these paths, leaving anything else you have staged
// alone.
func commit(root string, paths []string, msg string) error {
	if len(paths) == 0 {
		return nil
	}
	rel := make([]string, 0, len(paths))
	for _, p := range paths {
		r, err := filepath.Rel(root, p)
		if err != nil {
			r = p
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	ident := []string{}
	if out, _ := exec.Command("git", "-C", root, "config", "user.email").Output(); strings.TrimSpace(string(out)) == "" {
		ident = []string{"-c", "user.name=x-mem", "-c", "user.email=x-mem@localhost"}
	}
	run := func(args ...string) error {
		cmd := exec.Command("git", append(append([]string{"-C", root}, ident...), args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return errors.New(firstLine(msg))
		}
		return nil
	}
	if err := run(append([]string{"add", "-A", "--"}, rel...)...); err != nil {
		return err
	}
	if err := run(append([]string{"commit", "-q", "--no-verify", "-m", msg, "--"}, rel...)...); err != nil {
		_ = run(append([]string{"reset", "-q", "--"}, rel...)...)
		return err
	}
	return nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "hint:") {
			return l
		}
	}
	return s
}

// repoTracks reports whether git knows the path.
func repoTracks(root, rel string) bool {
	return exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", rel).Run() == nil
}
