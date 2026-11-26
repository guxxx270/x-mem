// Package index keeps a small on-disk cache so that a hook can answer in a
// few milliseconds. The Markdown files are the only source of truth: the cache
// can be deleted at any time. Every call compares file sizes and modification
// times with the cache and rebuilds it when anything changed, so hand edits
// take effect on the next query without any command.
//
// The cache is two files per combination of layers: a small metadata file
// (one record per note, the vocabulary and offsets) and a postings file read
// with ReadAt, so a query only reads the postings of its own terms.
package index

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/note"
	"x-mem/internal/textutil"
)

const formatVersion = 4

// Field weights: a term in the title or an alias counts three times a term in
// the body.
const (
	titleWeight = 3
	aliasWeight = 3
	bodyWeight  = 1
)

// Doc is what the index keeps about one note. The note body is not cached;
// Note() reads the file when a caller needs it.
type Doc struct {
	ID, Title, Type, Layer, Rel, Path string
	Aliases, Triggers                 []string
	Relations                         []note.Relation
	Size, MTime                       int64

	Active          bool     // false: superseded, deprecated, draft or outside its valid dates
	Reason          string   // why it is not active
	SupersededBy    string   // id of the note that replaced it
	SupersededLines []string // lines another note replaced; the rest still holds
	ConflictsWith   []string // ids of notes that disagree with it
	Problems        []string // relation problems found while linking notes
	NoteProblems    []string // problems in the note's own frontmatter
	Suspicious      bool     // the text reads like orders to the agent

	Len        float32         // weighted length in terms
	Chars      string          // the note's distinct Chinese characters
	TitleUnits []textutil.Unit // content units of the title
	Phrases    []string        // normalized aliases (and a long enough title)
	Updated    string          // YYYY-MM-DD used for the recency boost
	Summary    string          // first line still in force, for listings

	once sync.Once
	note *note.Note
}

// Index is the cached state for one combination of layers.
type Index struct {
	Version  int
	Key      string
	Day      string
	Docs     []*Doc
	N        int
	AvgLen   float64
	Vocab    []string // sorted terms
	Offsets  []uint32 // postings of Vocab[i] are entries Offsets[i]..Offsets[i+1]
	PostFile string

	dir     string
	post    *os.File
	pending []byte // postings not yet written to disk
	Rebuilt bool   // this call re-read the files
}

// Posting is one note that contains a term, with the term's weighted frequency.
type Posting struct {
	Doc int
	TF  float32
}

// Open loads the cache for these layers and brings it up to date with the
// files on disk.
func Open(l config.Layers) (*Index, error) {
	dir := l.State
	metaPath := filepath.Join(dir, "index-"+l.Key()+".meta")
	old := loadMeta(metaPath)
	sigs, err := scan(l)
	if err != nil {
		return nil, err
	}
	today := time.Now().Format("2006-01-02")
	if old != nil && old.Version == formatVersion && old.Day == today && sameFiles(old, sigs) {
		f, err := os.Open(filepath.Join(dir, old.PostFile))
		if err == nil {
			old.dir, old.post, old.Rebuilt = dir, f, false
			return old, nil
		}
	}
	ix, err := build(l, sigs, today)
	if err != nil {
		return nil, err
	}
	if err := ix.save(dir, metaPath, old); err != nil {
		return nil, err
	}
	ix.Rebuilt = true
	return ix, nil
}

// Rebuild drops the cache and reads every file again.
func Rebuild(l config.Layers) (*Index, error) {
	_ = os.Remove(filepath.Join(l.State, "index-"+l.Key()+".meta"))
	return Open(l)
}

// Close releases the postings file.
func (ix *Index) Close() {
	if ix.post != nil {
		ix.post.Close()
	}
}

type sig struct {
	layer, rel, path string
	size, mtime      int64
}

func scan(l config.Layers) ([]sig, error) {
	var out []sig
	for _, root := range l.Roots() {
		err := filepath.WalkDir(root.Dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if p != root.Dir && config.SkipDir(de.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(p), ".md") {
				return nil
			}
			info, err := de.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root.Dir, p)
			out = append(out, sig{root.Name, filepath.ToSlash(rel), p, info.Size(), info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sameFiles(ix *Index, sigs []sig) bool {
	if len(ix.Docs) != len(sigs) {
		return false
	}
	have := make(map[string]*Doc, len(ix.Docs))
	for _, d := range ix.Docs {
		have[d.Layer+"\x00"+d.Rel] = d
	}
	for _, s := range sigs {
		d, ok := have[s.layer+"\x00"+s.rel]
		if !ok || d.Size != s.size || d.MTime != s.mtime {
			return false
		}
	}
	return true
}

func loadMeta(path string) *Index {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ix Index
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&ix); err != nil {
		return nil
	}
	return &ix
}

// build parses every note, links relations, decides which notes are in force
// and indexes those that are.
func build(l config.Layers, sigs []sig, today string) (*Index, error) {
	ix := &Index{Version: formatVersion, Key: l.Key(), Day: today, dir: l.State}
	for _, s := range sigs {
		src, err := os.ReadFile(s.path)
		if err != nil {
			continue
		}
		n := note.Parse(string(src), s.rel)
		n.Layer, n.Path = s.layer, s.path
		d := &Doc{
			ID: n.ID, Title: n.Title, Type: n.Type, Layer: s.layer, Rel: n.Rel, Path: s.path,
			Aliases: n.Aliases, Triggers: n.Triggers, Relations: n.Relations,
			Size: s.size, MTime: s.mtime, NoteProblems: n.Problems,
			Suspicious: note.Suspicious(n.Title + "\n" + n.Body),
			note:       n,
		}
		d.once.Do(func() {})
		ix.Docs = append(ix.Docs, d)
	}
	sort.Slice(ix.Docs, func(i, j int) bool {
		a, b := ix.Docs[i], ix.Docs[j]
		if a.Layer != b.Layer {
			return a.Layer > b.Layer // project before personal
		}
		return a.Rel < b.Rel
	})
	ix.link(today)

	termDocs := map[string][]Posting{}
	total := 0.0
	for i, d := range ix.Docs {
		n := d.note
		d.Updated = datePart(n.ValidFrom)
		if d.Updated == "" {
			d.Updated = time.Unix(0, d.MTime).Format("2006-01-02")
		}
		lines := d.InForceLines()
		for _, l := range lines {
			if s := strings.TrimLeft(l, "-*> "); s != "" {
				d.Summary = cut(s, 80)
				break
			}
		}
		if !d.Active {
			continue
		}
		terms := map[string]float32{}
		add := func(text string, w float32) {
			for _, t := range textutil.Terms(text) {
				terms[t] += w
			}
		}
		add(n.Title, titleWeight)
		for _, a := range n.Aliases {
			add(a, aliasWeight)
		}
		body := strings.Join(lines, "\n")
		add(body, bodyWeight)
		for t, tf := range terms {
			termDocs[t] = append(termDocs[t], Posting{Doc: i, TF: tf})
			d.Len += tf
		}
		d.Chars = distinctCJK(n.Title + strings.Join(n.Aliases, " ") + body)
		d.TitleUnits = textutil.Units(n.Title)
		d.Phrases = phrases(n)
		ix.N++
		total += float64(d.Len)
	}
	if ix.N > 0 {
		ix.AvgLen = total / float64(ix.N)
	}
	ix.Vocab = make([]string, 0, len(termDocs))
	for t := range termDocs {
		ix.Vocab = append(ix.Vocab, t)
	}
	sort.Strings(ix.Vocab)
	ix.Offsets = make([]uint32, len(ix.Vocab)+1)
	var post bytes.Buffer
	var n uint32
	buf := make([]byte, 8)
	for i, t := range ix.Vocab {
		ix.Offsets[i] = n
		ps := termDocs[t]
		sort.Slice(ps, func(a, b int) bool { return ps[a].Doc < ps[b].Doc })
		for _, p := range ps {
			binary.LittleEndian.PutUint32(buf[0:4], uint32(p.Doc))
			binary.LittleEndian.PutUint32(buf[4:8], math.Float32bits(p.TF))
			post.Write(buf)
			n++
		}
	}
	ix.Offsets[len(ix.Vocab)] = n
	ix.pending = post.Bytes()
	return ix, nil
}

func (ix *Index) save(dir, metaPath string, old *Index) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ix.PostFile = fmt.Sprintf("index-%s-%d.post", ix.Key, time.Now().UnixNano())
	postPath := filepath.Join(dir, ix.PostFile)
	if err := os.WriteFile(postPath, ix.pending, 0o644); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(ix); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", metaPath, os.Getpid())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, metaPath); err != nil {
		return err
	}
	if old != nil && old.PostFile != "" && old.PostFile != ix.PostFile {
		_ = os.Remove(filepath.Join(dir, old.PostFile))
	}
	// Postings left behind by a crash or a lost race. Give readers that may
	// still hold one a minute before removing it.
	stale, _ := filepath.Glob(filepath.Join(dir, "index-"+ix.Key+"-*.post"))
	for _, p := range stale {
		if filepath.Base(p) == ix.PostFile {
			continue
		}
		if info, err := os.Stat(p); err == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(p)
		}
	}
	f, err := os.Open(postPath)
	if err != nil {
		return err
	}
	ix.post, ix.pending = f, nil
	return nil
}

// link resolves relations and decides which notes are in force today.
func (ix *Index) link(today string) {
	for _, d := range ix.Docs {
		d.Active = true
		switch d.note.Status {
		case "active", "":
		case "superseded", "deprecated":
			d.Active, d.Reason = false, "status: "+d.note.Status
		case "draft":
			d.Active, d.Reason = false, "草稿，尚未生效"
		default:
			d.Problems = append(d.Problems, fmt.Sprintf("status %q 不认识，按 active 处理", d.note.Status))
		}
		if until := datePart(d.note.ValidUntil); until != "" && until <= today {
			d.Active, d.Reason = false, "已过期（valid_until "+until+"）"
		}
		if from := datePart(d.note.ValidFrom); from != "" && from > today {
			d.Active, d.Reason = false, "尚未生效（valid_from "+from+"）"
		}
	}
	for _, d := range ix.Docs {
		if d.note.Status == "draft" {
			continue
		}
		for _, r := range d.Relations {
			t := ix.Lookup(r.Target)
			switch {
			case t == nil:
				d.Problems = append(d.Problems, fmt.Sprintf("%s 指向的 %q 找不到", r.Op, r.Target))
				continue
			case t == d:
				d.Problems = append(d.Problems, fmt.Sprintf("%s 指向了自己", r.Op))
				continue
			}
			switch r.Op {
			case "@supersedes", "@deprecated":
				if r.Line != "" {
					if !hasLine(t.note, r.Line) {
						d.Problems = append(d.Problems, fmt.Sprintf("%s 要取代的那一行在 %q 里找不到：%s", r.Op, t.Title, r.Line))
						continue
					}
					t.SupersededLines = append(t.SupersededLines, r.Line)
					continue
				}
				if t.Active {
					t.Active = false
					if r.Op == "@supersedes" {
						t.Reason, t.SupersededBy = "已被取代："+d.Title, d.ID
					} else {
						t.Reason = "已作废：" + d.Title
					}
				}
			case "@conflicts_with":
				d.ConflictsWith = append(d.ConflictsWith, t.ID)
				t.ConflictsWith = append(t.ConflictsWith, d.ID)
			}
		}
	}
}

// Note returns the parsed note, reading the file on first use. If the file
// is gone it returns a stub built from the cached fields.
func (d *Doc) Note() *note.Note {
	d.once.Do(func() {
		if src, err := os.ReadFile(d.Path); err == nil {
			d.note = note.Parse(string(src), d.Rel)
			d.note.Layer, d.note.Path = d.Layer, d.Path
			return
		}
		d.note = &note.Note{ID: d.ID, Title: d.Title, Type: d.Type, Layer: d.Layer, Rel: d.Rel, Path: d.Path}
	})
	return d.note
}

// IsRule reports whether the note is listed at session start.
func (d *Doc) IsRule() bool {
	switch d.Type {
	case "rule", "preference", "policy", "convention":
		return true
	}
	return false
}

// InForceLines returns the body lines that no other note has replaced.
func (d *Doc) InForceLines() []string {
	var out []string
	for _, l := range d.Note().BodyLines() {
		if !lineReplaced(l, d.SupersededLines) {
			out = append(out, l)
		}
	}
	return out
}

// LineReplaced reports whether a body line was replaced by another note.
func (d *Doc) LineReplaced(line string) bool { return lineReplaced(line, d.SupersededLines) }

func lineReplaced(line string, replaced []string) bool {
	nl := textutil.Normalize(line)
	for _, r := range replaced {
		if nr := textutil.Normalize(strings.TrimSpace(r)); nr != "" && strings.Contains(nl, nr) {
			return true
		}
	}
	return false
}

func hasLine(n *note.Note, line string) bool {
	for _, l := range n.BodyLines() {
		if lineReplaced(l, []string{line}) {
			return true
		}
	}
	return false
}

func phrases(n *note.Note) []string {
	var out []string
	for _, a := range n.Aliases {
		p := strings.TrimSpace(textutil.Normalize(a))
		if textutil.CJKLen(p) >= 2 || (textutil.CJKLen(p) == 0 && len(p) >= 3) {
			out = append(out, p)
		}
	}
	t := strings.TrimSpace(textutil.Normalize(n.Title))
	words := len(strings.Fields(t))
	if textutil.CJKLen(t) >= 3 || (textutil.CJKLen(t) == 0 && (words >= 2 || len(t) >= 6)) {
		out = append(out, t)
	}
	return out
}

func distinctCJK(s string) string {
	seen := map[rune]bool{}
	var b strings.Builder
	for _, r := range s {
		if textutil.IsCJK(r) && !seen[r] {
			seen[r] = true
			b.WriteRune(r)
		}
	}
	return b.String()
}

func datePart(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return ""
}

func cut(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "……"
}

// Lookup finds a note by id, file name, relative path or title.
func (ix *Index) Lookup(ref string) *Doc {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	for _, d := range ix.Docs {
		if d.ID == ref {
			return d
		}
	}
	clean := strings.TrimSuffix(filepath.ToSlash(ref), ".md")
	for _, d := range ix.Docs {
		rel := strings.TrimSuffix(d.Rel, ".md")
		stem := rel[strings.LastIndex(rel, "/")+1:]
		if rel == clean || stem == clean || d.Path == ref {
			return d
		}
	}
	for _, d := range ix.Docs {
		if strings.EqualFold(d.Title, ref) {
			return d
		}
	}
	return nil
}

// DF is the number of notes in force that contain a term.
func (ix *Index) DF(t string) int {
	i := sort.SearchStrings(ix.Vocab, t)
	if i >= len(ix.Vocab) || ix.Vocab[i] != t {
		return 0
	}
	return int(ix.Offsets[i+1] - ix.Offsets[i])
}

// IDF is the BM25 inverse document frequency of a term.
func (ix *Index) IDF(t string) float64 {
	df := float64(ix.DF(t))
	return math.Log(1 + (float64(ix.N)-df+0.5)/(df+0.5))
}

// Postings lists the notes that contain a term.
func (ix *Index) Postings(t string) []Posting {
	i := sort.SearchStrings(ix.Vocab, t)
	if i >= len(ix.Vocab) || ix.Vocab[i] != t {
		return nil
	}
	from, to := ix.Offsets[i], ix.Offsets[i+1]
	raw := make([]byte, (to-from)*8)
	if ix.pending != nil {
		copy(raw, ix.pending[from*8:to*8])
	} else if ix.post == nil {
		return nil
	} else if _, err := ix.post.ReadAt(raw, int64(from)*8); err != nil {
		return nil
	}
	out := make([]Posting, 0, to-from)
	for k := 0; k < len(raw); k += 8 {
		out = append(out, Posting{
			Doc: int(binary.LittleEndian.Uint32(raw[k : k+4])),
			TF:  math.Float32frombits(binary.LittleEndian.Uint32(raw[k+4 : k+8])),
		})
	}
	return out
}
