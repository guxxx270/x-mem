package index_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/index"
)

func setup(t *testing.T) (config.Layers, string) {
	t.Helper()
	home := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state")}
	dir := filepath.Join(l.Personal, "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "a.md")
	write(t, p, "---\nid: a\ntitle: 发版日\n---\n每周五发版。\n")
	return l, p
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandEditTakesEffect(t *testing.T) {
	l, p := setup(t)
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	if !ix.Rebuilt || ix.DF("周五") != 1 {
		t.Fatalf("first open should build; df(周五)=%d", ix.DF("周五"))
	}
	ix.Close()

	ix, _ = index.Open(l)
	if ix.Rebuilt {
		t.Fatal("unchanged files must reuse the cache")
	}
	ix.Close()

	write(t, p, "---\nid: a\ntitle: 发版日\n---\n每周二上午发版。\n")
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, future, future)
	ix, _ = index.Open(l)
	defer ix.Close()
	if !ix.Rebuilt || ix.DF("周五") != 0 || ix.DF("周二") != 1 {
		t.Fatalf("edit not picked up: rebuilt=%v df(周五)=%d df(周二)=%d", ix.Rebuilt, ix.DF("周五"), ix.DF("周二"))
	}
	if ps := ix.Postings("周二"); len(ps) != 1 || ps[0].TF <= 0 {
		t.Fatalf("postings = %v", ps)
	}
}

func TestLostPostingsRebuild(t *testing.T) {
	l, _ := setup(t)
	ix, _ := index.Open(l)
	ix.Close()
	posts, _ := filepath.Glob(filepath.Join(l.State, "*.post"))
	for _, p := range posts {
		os.Remove(p)
	}
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if !ix.Rebuilt || len(ix.Postings("发版")) != 1 {
		t.Fatal("a missing postings file must trigger a rebuild")
	}
}

func TestNoteBodyIsReadLazily(t *testing.T) {
	l, p := setup(t)
	ix, _ := index.Open(l)
	ix.Close()
	ix, _ = index.Open(l)
	defer ix.Close()
	d := ix.Lookup("a")
	if d == nil || d.Note().Body == "" || d.Summary != "每周五发版。" {
		t.Fatalf("lookup/lazy read failed: %+v", d)
	}
	_ = p
}
