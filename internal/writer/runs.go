package writer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"x-mem/internal/note"
)

// Op is one change a write made, recorded so it can be reversed on its own.
// Undo reverses recorded file operations rather than running git revert, so
// reverting an earlier write never conflicts with later ones.
type Op struct {
	Kind    string `json:"kind"` // create, status, ignore
	Path    string `json:"path"`
	Content string `json:"content,omitempty"` // create: what was written
	From    string `json:"from,omitempty"`    // status: before ("" = no field)
	To      string `json:"to,omitempty"`      // status: after
	Line    string `json:"line,omitempty"`    // ignore: the .gitignore line added

	before  []byte // the file as it was, for rolling back an unfinished write
	existed bool
}

// Run is one write.
type Run struct {
	ID        string    `json:"id"`
	Proposal  string    `json:"proposal"`
	Title     string    `json:"title"`
	At        time.Time `json:"at"`
	Root      string    `json:"git_root,omitempty"`
	Committed bool      `json:"committed"`
	Ops       []Op      `json:"ops"`
	Undone    bool      `json:"undone,omitempty"`
	UndoneAt  time.Time `json:"undone_at,omitempty"`
	UndoRun   string    `json:"undo_commit_note,omitempty"`
}

func runPath(state, id string) string { return filepath.Join(state, "runs", id+".json") }

func saveRun(state string, r *Run) error {
	if err := os.MkdirAll(filepath.Join(state, "runs"), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := runPath(state, r.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadRun reads a run record (a unique prefix of the id is enough).
func LoadRun(state, id string) (*Run, error) {
	path := runPath(state, id)
	if _, err := os.Stat(path); err != nil {
		matches, _ := filepath.Glob(filepath.Join(state, "runs", id+"*.json"))
		if len(matches) != 1 {
			return nil, fmt.Errorf("没有这次写入：%s（x-mem log 列出最近的写入）", id)
		}
		path = matches[0]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Runs lists the writes, newest first.
func Runs(state string) []*Run {
	files, _ := filepath.Glob(filepath.Join(state, "runs", "*.json"))
	var out []*Run
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r Run
		if json.Unmarshal(data, &r) == nil {
			out = append(out, &r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// tx applies ops and can roll all of them back if a later step fails.
type tx struct{ ops []Op }

func (t *tx) create(path, content string) error {
	if fileExists(path) {
		return fmt.Errorf("文件已存在：%s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	t.ops = append(t.ops, Op{Kind: "create", Path: path, Content: content})
	return nil
}

func (t *tx) setStatus(path, status string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, old := note.SetStatus(string(src), status)
	if err := writeFile(path, []byte(out)); err != nil {
		return err
	}
	t.ops = append(t.ops, Op{Kind: "status", Path: path, From: old, To: status, before: src, existed: true})
	return nil
}

func (t *tx) ignore(path, line string) error {
	src, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(src), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	s := string(src)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if err := writeFile(path, []byte(s+line+"\n")); err != nil {
		return err
	}
	t.ops = append(t.ops, Op{Kind: "ignore", Path: path, Line: line, before: src, existed: existed})
	return nil
}

// paths is what to commit. A confidential note itself is never committed:
// only the .gitignore line that keeps it out.
func (t *tx) paths(confidential bool, notePath string) []string {
	var out []string
	for _, op := range t.ops {
		if confidential && op.Path == notePath {
			continue
		}
		out = append(out, op.Path)
	}
	return out
}

func (t *tx) rollback() {
	for i := len(t.ops) - 1; i >= 0; i-- {
		op := t.ops[i]
		switch {
		case op.Kind == "create":
			os.Remove(op.Path)
		case op.existed:
			writeFile(op.Path, op.before)
		default:
			os.Remove(op.Path)
		}
	}
	t.ops = nil
}

func writeFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := fmt.Sprintf("%s.x-mem-%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Undo reverses one write and commits that as a new change. It checks every
// file first and changes nothing if any of them was edited since in a way
// that clashes; it then says which file and why.
func (w *Writer) Undo(id string) (*Run, error) {
	lock, err := Acquire(w.L.State, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	r, err := LoadRun(w.L.State, id)
	if err != nil {
		return nil, err
	}
	if r.Undone {
		return r, fmt.Errorf("run %s 已经撤回过了", r.ID)
	}
	type change struct {
		path    string
		data    []byte
		remove  bool
		before  []byte
		existed bool
	}
	var changes []change
	var clashes []string
	for i := len(r.Ops) - 1; i >= 0; i-- {
		op := r.Ops[i]
		cur, err := os.ReadFile(op.Path)
		exists := err == nil
		switch op.Kind {
		case "create":
			if !exists {
				continue // already gone
			}
			if string(cur) != op.Content {
				clashes = append(clashes, op.Path+" 写入后被改过；要删就手动删")
				continue
			}
			changes = append(changes, change{path: op.Path, remove: true, before: cur, existed: true})
		case "status":
			if !exists {
				clashes = append(clashes, op.Path+" 不见了，没法把状态改回去")
				continue
			}
			if got := note.StatusOf(string(cur)); got != op.To {
				clashes = append(clashes, fmt.Sprintf("%s 的状态现在是 %q，不是这次写入改成的 %q", op.Path, got, op.To))
				continue
			}
			var out string
			if op.From == "" {
				out = note.RemoveStatus(string(cur))
			} else {
				out, _ = note.SetStatus(string(cur), op.From)
			}
			changes = append(changes, change{path: op.Path, data: []byte(out), before: cur, existed: true})
		case "ignore":
			if !exists {
				continue
			}
			var keep []string
			for _, l := range strings.Split(string(cur), "\n") {
				if strings.TrimSpace(l) != op.Line {
					keep = append(keep, l)
				}
			}
			changes = append(changes, change{path: op.Path, data: []byte(strings.Join(keep, "\n")), before: cur, existed: true})
		}
	}
	if len(clashes) > 0 {
		return r, errors.New("撤回失败，什么都没改：" + strings.Join(clashes, "；"))
	}
	var done []change
	restore := func() {
		for _, c := range done {
			writeFile(c.path, c.before)
		}
	}
	var paths []string
	for _, c := range changes {
		var err error
		if c.remove {
			err = os.Remove(c.path)
		} else {
			err = writeFile(c.path, c.data)
		}
		if err != nil {
			restore()
			return r, fmt.Errorf("撤回失败：%v", err)
		}
		done = append(done, c)
		paths = append(paths, c.path)
	}
	if r.Committed && r.Root != "" {
		var commitPaths []string
		for _, p := range paths {
			if gitTracksOrExists(r.Root, p) {
				commitPaths = append(commitPaths, p)
			}
		}
		if err := commit(r.Root, commitPaths, fmt.Sprintf("x-mem: 撤回 %s（%s）", r.Title, r.ID)); err != nil {
			restore()
			return r, fmt.Errorf("撤回失败，提交没成功，文件已还原：%v", err)
		}
	}
	r.Undone, r.UndoneAt = true, w.Now()
	return r, saveRun(w.L.State, r)
}

// gitTracksOrExists leaves out confidential notes, which git never saw.
func gitTracksOrExists(root, path string) bool {
	if fileExists(path) {
		return true
	}
	rel, err := repoRel(root, path)
	if err != nil {
		return false
	}
	return repoTracks(root, filepath.ToSlash(rel))
}
