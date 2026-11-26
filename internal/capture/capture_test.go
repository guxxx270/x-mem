package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/writer"
)

func TestCandidates(t *testing.T) {
	keep := []string{
		"以后数据库迁移脚本一律先在测试库跑一遍",
		"发版改到每周二上午，因为测试周一才跑完",
		"记住：日志统一用 zap，不要再用 logrus",
		"From now on always run the full test suite before pushing",
	}
	drop := []string{"好的", "继续", "嗯", "好的，继续", "ok", "帮我看看这个报错", "这个函数是干嘛的？",
		"<private>以后密码统一放在保险柜里</private>", "```go\n// 以后都这样写\nfunc main() {}\n```"}
	for _, s := range keep {
		if len(Candidates(s)) == 0 {
			t.Errorf("should be a candidate: %s", s)
		}
	}
	for _, s := range drop {
		if c := Candidates(s); len(c) != 0 {
			t.Errorf("should be dropped: %s → %v", s, c)
		}
	}
}

type session struct {
	t    *testing.T
	path string
	sid  string
}

func newSession(t *testing.T, dir, sid, cwd string) *session {
	p := filepath.Join(dir, "-proj", sid+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, nil, 0o644)
	return &session{t, p, sid}
}

func (s *session) add(role, text, cwd string) {
	var content any = text
	if role == "assistant" {
		content = []map[string]string{{"type": "text", "text": text}}
	}
	line, _ := json.Marshal(map[string]any{
		"type": role, "sessionId": s.sid, "cwd": cwd, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": role, "content": content},
	})
	f, _ := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write(append(line, '\n'))
	f.Close()
}

func setup(t *testing.T) (config.Layers, *writer.Writer, string) {
	home := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	os.MkdirAll(filepath.Join(l.Personal, "notes"), 0o755)
	tr := t.TempDir()
	t.Setenv("X_MEM_TRANSCRIPTS", tr)
	w := writer.New(l)
	w.Settings.Host = []string{"false"} // no host model: raw candidates
	if err := SaveState(l, State{Enabled: true, Since: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return l, w, tr
}

func pending(l config.Layers) []*writer.Proposal {
	return writer.NewInbox(l.State).List(writer.Review, writer.Queued)
}

func TestDecisionsReachInboxAcksDoNot(t *testing.T) {
	l, w, tr := setup(t)
	s := newSession(t, tr, "s1", "/work/app")
	s.add("user", "帮我把登录页改一下", "/work/app")
	s.add("assistant", "改好了。要不要顺便统一一下日志库？", "/work/app")
	s.add("user", "好的", "/work/app")
	s.add("user", "继续", "/work/app")
	s.add("user", "以后日志统一用 zap，不要再用 logrus", "/work/app")
	AddSignal(l, Signal{Path: s.path})
	n, err := Run(l, w, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("want one proposal, got %d %v", n, err)
	}
	ps := pending(l)
	if len(ps) != 1 || !strings.Contains(ps[0].Text, "zap") || ps[0].Status != writer.Review || ps[0].Kind != "rule" {
		t.Fatalf("unexpected inbox: %+v", ps)
	}
	if ps[0].Context == "" || ps[0].Session != "claude:s1" {
		t.Fatalf("missing context or session: %+v", ps[0])
	}
	// Nothing new: nothing read again.
	AddSignal(l, Signal{Path: s.path})
	if n, _ := Run(l, w, time.Now()); n != 0 {
		t.Fatalf("the same lines were read twice")
	}
}

func TestPrivateAndOptOutNeverEnter(t *testing.T) {
	l, w, tr := setup(t)
	s := newSession(t, tr, "s2", "/work/app")
	s.add("user", "这段别记 <private>以后服务器密码统一放在 1Password 里</private> 你看着办", "/work/app")
	AddSignal(l, Signal{Path: s.path})
	if n, _ := Run(l, w, time.Now()); n != 0 {
		t.Fatalf("private text became a proposal")
	}
	o := newSession(t, tr, "s3", "/work/app")
	o.add("user", "以后发版统一放在周二上午", "/work/app")
	AddSignal(l, Signal{Path: o.path})
	Run(l, w, time.Now())
	if len(pending(l)) != 1 {
		t.Fatalf("setup: want one pending proposal")
	}
	o.add("user", "#不记 这个会话别记了", "/work/app")
	o.add("user", "以后数据库统一用 PostgreSQL", "/work/app")
	AddSignal(l, Signal{Path: o.path})
	Run(l, w, time.Now())
	if ps := pending(l); len(ps) != 0 {
		t.Fatalf("an opted-out session left proposals: %+v", ps[0])
	}
	o.add("user", "以后缓存统一用 Redis", "/work/app")
	AddSignal(l, Signal{Path: o.path})
	if n, _ := Run(l, w, time.Now()); n != 0 {
		t.Fatal("an opted-out session is read again")
	}
	notes, _ := os.ReadDir(filepath.Join(l.Personal, "notes"))
	if len(notes) != 0 {
		t.Fatal("something reached the notes")
	}
}

func TestHostDistils(t *testing.T) {
	l, w, tr := setup(t)
	script := filepath.Join(t.TempDir(), "host.sh")
	os.WriteFile(script, []byte(`#!/bin/sh
cat >/dev/null
[ -n "$X_MEM_DISABLE" ] || exit 3
echo '好的，结果如下：[{"n":1,"keep":false},{"n":2,"keep":true,"kind":"decision","title":"发版日","text":"发版改到每周二上午，因为测试周一才跑完。","quote":"发版改到每周二上午"}]'
`), 0o755)
	w.Settings.Host = []string{script}
	s := newSession(t, tr, "s4", "/work/app")
	s.add("user", "以后这个函数先别动", "/work/app")
	s.add("user", "发版改到每周二上午，因为测试周一才跑完", "/work/app")
	AddSignal(l, Signal{Path: s.path})
	n, err := Run(l, w, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("want one distilled proposal, got %d %v", n, err)
	}
	p := pending(l)[0]
	if p.Title != "发版日" || p.Quote != "发版改到每周二上午" {
		t.Fatalf("host output not used: %+v", p)
	}
}

func TestCursorReadsOnlyNewLines(t *testing.T) {
	l, w, tr := setup(t)
	s := newSession(t, tr, "long", "/work/app")
	for i := 0; i < 500; i++ {
		s.add("user", fmt.Sprintf("第 %d 轮，看看这个", i), "/work/app")
		s.add("assistant", "好的", "/work/app")
	}
	AddSignal(l, Signal{Path: s.path})
	Run(l, w, time.Now())
	cursors := map[string]*cursor{}
	readJSON(cursorPath(l), &cursors)
	info, _ := os.Stat(s.path)
	if cursors[s.path].Offset != info.Size() {
		t.Fatalf("cursor %d, file %d", cursors[s.path].Offset, info.Size())
	}
	s.add("user", "以后提交前一律先跑 make lint", "/work/app")
	AddSignal(l, Signal{Path: s.path})
	if n, _ := Run(l, w, time.Now()); n != 1 {
		t.Fatalf("the new line was not picked up")
	}
}
