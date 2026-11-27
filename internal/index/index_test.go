package index_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/extract"
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

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	write(t, dst, string(data))
}

// Documents in the knowledge base are read in the background: until then
// they are listed as waiting, not searchable, and a hook never waits.
func TestDocumentsAreReadInTheBackground(t *testing.T) {
	l, p := setup(t)
	kb := t.TempDir()
	copyFile(t, "../extract/testdata/sample.docx", filepath.Join(kb, "规程/档案规程.docx"))
	copyFile(t, "../extract/testdata/sample.pdf", filepath.Join(kb, "规程/档案规程.pdf"))
	write(t, filepath.Join(kb, "~$档案规程.docx"), "lock file")
	l.KB = []config.KB{{Path: kb}}

	pending := 0
	index.OnPending = func(config.Layers) { pending++ }
	defer func() { index.OnPending = nil }()
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Waiting != 2 || pending != 1 || ix.DF("档案") != 0 {
		t.Fatalf("waiting %d, pending calls %d, df %d", ix.Waiting, pending, ix.DF("档案"))
	}
	ix.Close()

	// The background reader fills the text cache, then drops the cache of
	// the knowledge base.
	for _, f := range []string{"规程/档案规程.docx", "规程/档案规程.pdf"} {
		path := filepath.Join(kb, f)
		st, _ := os.Stat(path)
		text, err := extract.Text(path)
		extract.Store(l.State, path, st.Size(), st.ModTime().UnixNano(), text, err)
	}
	index.InvalidateKB(l)
	ix, _ = index.Open(l)
	if ix.Waiting != 0 || ix.DF("归档") != 2 {
		t.Fatalf("after reading: waiting %d, df(归档) %d", ix.Waiting, ix.DF("归档"))
	}
	var doc *index.Doc
	for _, d := range ix.Docs {
		if d.Layer == "kb" && filepath.Ext(d.Path) == ".pdf" {
			doc = d
		}
	}
	if doc == nil || doc.Title != "档案规程" || !doc.Active {
		t.Fatalf("pdf doc: %+v", doc)
	}
	if body := doc.Note().Body; !strings.Contains(body, "三十日内归档") {
		t.Fatalf("lazy body from the text cache: %q", body)
	}
	ix.Close()

	// Writing a note rebuilds the memory, not the knowledge base.
	time.Sleep(10 * time.Millisecond)
	write(t, p, "---\nid: a\ntitle: 发版日\n---\n每周四发版。\n")
	ix, _ = index.Open(l)
	defer ix.Close()
	if !ix.Rebuilt || ix.DF("周四") != 1 || ix.DF("归档") != 2 {
		t.Fatalf("memory rebuilt with kb intact: rebuilt %v df(周四) %d df(归档) %d", ix.Rebuilt, ix.DF("周四"), ix.DF("归档"))
	}
}

func TestUnreadableDocumentIsReported(t *testing.T) {
	l, _ := setup(t)
	kb := t.TempDir()
	write(t, filepath.Join(kb, "坏文件.pdf"), "not a pdf at all")
	l.KB = []config.KB{{Path: kb}}
	index.ExtractInline = true
	defer func() { index.ExtractInline = false }()
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	for _, d := range ix.Docs {
		if d.Layer == "kb" {
			if d.Active || !strings.HasPrefix(d.Unread, "读不出文字") || d.Waiting {
				t.Fatalf("bad pdf: %+v", d)
			}
			return
		}
	}
	t.Fatal("bad pdf not listed")
}
