package writer

import (
	"errors"
	"fmt"
	"time"

	"x-mem/internal/index"
)

// Retire takes a note out of force (status: deprecated) without deleting
// it, as one write that x-mem undo can reverse. Skills you no longer want
// are retired this way.
func (w *Writer) Retire(id string) (*Run, error) {
	lock, err := Acquire(w.L.State, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	ix, err := index.Open(w.L)
	if err != nil {
		return nil, err
	}
	defer ix.Close()
	d := ix.Lookup(id)
	switch {
	case d == nil:
		return nil, fmt.Errorf("找不到 %q（用 id、文件名或标题）", id)
	case d.Layer == "kb":
		return nil, errors.New("知识库里的文件不归 x-mem 管，不能停用")
	case !d.Active:
		return nil, fmt.Errorf("「%s」已经不生效了（%s）", d.Title, d.Reason)
	}
	now := w.Now()
	run := &Run{ID: RunID(now), Title: "停用：" + d.Title, At: now}
	for fileExists(runPath(w.L.State, run.ID)) {
		run.ID = RunID(now)
	}
	t := &tx{}
	if err := t.setStatus(d.Path, "deprecated"); err != nil {
		return nil, err
	}
	run.Ops = t.ops
	root := w.L.Personal
	if d.Layer == "project" {
		root = w.L.Project
	}
	if gitRoot := repoRoot(root); gitRoot != "" && (d.Layer == "personal" || w.Settings.CommitProject) {
		run.Root = gitRoot
		if err := commit(gitRoot, []string{d.Path}, fmt.Sprintf("x-mem: 停用 %s（run %s）", d.Title, run.ID)); err != nil {
			t.rollback()
			return nil, fmt.Errorf("提交失败：%v", err)
		}
		run.Committed = true
	}
	if err := saveRun(w.L.State, run); err != nil {
		t.rollback()
		return nil, err
	}
	return run, nil
}
