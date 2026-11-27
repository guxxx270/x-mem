// Package index keeps a small on-disk cache so that a hook can answer in a
// few milliseconds. The Markdown files are the only source of truth: the cache
// can be deleted at any time. Every call compares file sizes and modification
// times with the cache and rebuilds it when anything changed, so hand edits
// take effect on the next query without any command.
//
// The cache is two files per combination of layers: a small metadata file
// (one record per note, the vocabulary and offsets) and a postings file read
// with ReadAt, so a query only reads the postings of its own terms.
//
// Memory (personal and project notes) and the knowledge base are cached
// apart: writing a note rebuilds only the memory part, never the documents,
// and documents (Word, PDF, …) are read once into a text cache, in the
// background when a hook is waiting.
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
	"x-mem/internal/extract"
	"x-mem/internal/note"
	"x-mem/internal/textutil"
)

const formatVersion = 8

// ExtractInline makes Open read documents that have no cached text yet
// (x-mem kb add, reindex). Otherwise they wait for a background run, so a
// hook never waits on a PDF.
var ExtractInline bool

// OnPending is called when knowledge-base documents are waiting to be read;
// the command line starts the background reader here.
var OnPending func(l config.Layers)

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
	Enforcement, Sensitivity          string
	Links                             []string // [[wiki links]] in the body
	Session                           string   // source.session: the conversation it came from
	When                              string   // skill: the situation it is for
	Sources                           []string // skill: the notes it grew from
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

	TextFile string // a document read into the text cache: where its text is
	Unread   string // why a document has no text (yet): it is then not in force
	Waiting  bool   // its text has not been read yet

	once sync.Once
	note *note.Note
}

// part is the cached state for one group of layers: the memory (personal
// and project) or the knowledge base.
type part struct {
	Version  int
	Key      string
	Day      string // the memory is rebuilt daily (notes come into and out of force)
	Docs     []*Doc
	N        int
	AvgLen   float64
	Vocab    []string // sorted terms
	Offsets  []uint32 // postings of Vocab[i] are entries Offsets[i]..Offsets[i+1]
	PostFile string

	dir     string
	post    *os.File
	pending []byte // postings not yet written to disk
	rebuilt bool
	waiting int // knowledge base: documents not read yet
}

// Index is what a query sees: the memory and the knowledge base together.
type Index struct {
	Docs    []*Doc // memory first, then the knowledge base
	N       int
	AvgLen  float64
	Day     string
	Rebuilt bool // this call re-read files
	Waiting int  // knowledge-base documents still being read

	mem, kb *part
}

// Posting is one note that contains a term, with the term's weighted frequency.
type Posting struct {
	Doc int
	TF  float32
}

// Open loads the cache for these layers and brings it up to date with the
// files on disk.
func Open(l config.Layers) (*Index, error) {
	today := time.Now().Format("2006-01-02")
	memL := l
	memL.KB = nil
	mem, err := openPart(memL, today, false)
	if err != nil {
		return nil, err
	}
	ix := &Index{mem: mem, Day: today}
	if kbL := KBLayers(l); len(kbL.KB) > 0 {
		kb, err := openPart(kbL, today, true)
		if err != nil {
			mem.close()
			return nil, err
		}
		ix.kb = kb
		if kb.waiting > 0 && !ExtractInline && OnPending != nil && !extract.Busy(l.State) {
			OnPending(l)
		}
	}
	ix.combine()
	return ix, nil
}

// KBLayers is the knowledge-base part of a set of layers.
func KBLayers(l config.Layers) config.Layers {
	return config.Layers{Home: l.Home, State: l.State, KB: l.KB}
}

// InvalidateKB makes the next Open read the knowledge base again (after the
// background reader has filled the text cache).
func InvalidateKB(l config.Layers) {
	_ = os.Remove(metaPath(KBLayers(l)))
}

// metaPath names the cache by format version too, so two x-mem versions
// side by side (a plugin in one app, an older install in another) keep
// their own caches instead of rebuilding each other's.
func metaPath(l config.Layers) string {
	return filepath.Join(l.State, fmt.Sprintf("index-%s-v%d.meta", l.Key(), formatVersion))
}

func openPart(l config.Layers, today string, kb bool) (*part, error) {
	dir := l.State
	mp := metaPath(l)
	old := loadMeta(mp)
	sigs, err := scan(l, kb)
	if err != nil {
		return nil, err
	}
	day := today
	if kb {
		day = "" // reference material does not come into or out of force
	}
	if old != nil && old.Version == formatVersion && old.Day == day && sameFiles(old, sigs) {
		f, err := os.Open(filepath.Join(dir, old.PostFile))
		if err == nil {
			old.dir, old.post, old.rebuilt = dir, f, false
			for _, d := range old.Docs {
				if d.Waiting {
					old.waiting++
				}
			}
			return old, nil
		}
	}
	p, err := build(l, sigs, today, kb)
	if err != nil {
		return nil, err
	}
	p.Day = day
	if err := p.save(dir, mp, old); err != nil {
		return nil, err
	}
	p.rebuilt = true
	return p, nil
}

// combine lays the two parts side by side for queries.
func (ix *Index) combine() {
	ix.Docs = append([]*Doc{}, ix.mem.Docs...)
	ix.N, ix.AvgLen, ix.Rebuilt = ix.mem.N, ix.mem.AvgLen, ix.mem.rebuilt
	if ix.kb == nil {
		return
	}
	ix.Docs = append(ix.Docs, ix.kb.Docs...)
	ix.N += ix.kb.N
	if ix.N > 0 {
		ix.AvgLen = (ix.mem.AvgLen*float64(ix.mem.N) + ix.kb.AvgLen*float64(ix.kb.N)) / float64(ix.N)
	}
	ix.Rebuilt = ix.Rebuilt || ix.kb.rebuilt
	ix.Waiting = ix.kb.waiting
	// A note pointing at a knowledge-base page: say so, rather than "not
	// found" (the memory part was linked on its own).
	for _, d := range ix.mem.Docs {
		for _, r := range d.Relations {
			if lookupIn(ix.mem.Docs, r.Target) != nil {
				continue
			}
			if t := lookupIn(ix.kb.Docs, r.Target); t != nil {
				missing := fmt.Sprintf("%s 指向的 %q 找不到", r.Op, r.Target)
				for i, p := range d.Problems {
					if p == missing {
						d.Problems[i] = fmt.Sprintf("%s 指向的「%s」是知识库里的资料，不是记忆", r.Op, t.Title)
					}
				}
			}
		}
	}
}

// Rebuild drops the cache and reads every file again.
func Rebuild(l config.Layers) (*Index, error) {
	memL := l
	memL.KB = nil
	_ = os.Remove(metaPath(memL))
	_ = os.Remove(metaPath(KBLayers(l)))
	return Open(l)
}

// Close releases the postings files.
func (ix *Index) Close() {
	ix.mem.close()
	if ix.kb != nil {
		ix.kb.close()
	}
}

func (p *part) close() {
	if p.post != nil {
		p.post.Close()
	}
}

type sig struct {
	layer, rel, path string
	size, mtime      int64
}

func scan(l config.Layers, kb bool) ([]sig, error) {
	var out []sig
	for _, root := range l.Roots() {
		if (root.Name == "kb") != kb {
			continue
		}
		err := filepath.WalkDir(root.Dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if p != root.Dir {
					rel, _ := filepath.Rel(root.Dir, p)
					if config.SkipDir(de.Name()) || root.Excluded(rel) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".md" && !(kb && extract.Supported(ext)) {
				return nil
			}
			if name := de.Name(); strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") {
				return nil // Office lock files, hidden files
			}
			info, err := de.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root.Dir, p)
			if root.Excluded(rel) {
				return nil
			}
			out = append(out, sig{root.Name, filepath.ToSlash(rel), p, info.Size(), info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sameFiles(ix *part, sigs []sig) bool {
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

func loadMeta(path string) *part {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ix part
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&ix); err != nil {
		return nil
	}
	return &ix
}

// build parses every note, links relations, decides which notes are in force
// and indexes those that are.
func build(l config.Layers, sigs []sig, today string, kb bool) (*part, error) {
	ix := &part{Version: formatVersion, Key: l.Key(), Day: today, dir: l.State}
	for _, s := range sigs {
		var n *note.Note
		var textFile, unread string
		waiting := false
		if strings.EqualFold(filepath.Ext(s.path), ".md") {
			src, err := os.ReadFile(s.path)
			if err != nil {
				continue
			}
			n = note.Parse(string(src), s.rel)
		} else {
			// A document: its text comes from the text cache.
			text, errMsg, ok := extract.Cached(l.State, s.path, s.size, s.mtime)
			if !ok && ExtractInline {
				t, err := extract.Text(s.path)
				_ = extract.Store(l.State, s.path, s.size, s.mtime, t, err)
				text, ok = t, true
				if err != nil {
					errMsg = err.Error()
				}
			}
			n = note.Parse("", s.rel)
			n.Title = strings.TrimSuffix(filepath.Base(s.rel), filepath.Ext(s.rel))
			n.Type = "document"
			textFile = extract.CacheFile(l.State, s.path)
			switch {
			case !ok:
				unread, waiting = "还没读出文字（后台正在读）", true
				ix.waiting++
			case errMsg != "":
				unread = "读不出文字：" + errMsg
			default:
				n.Body = text
			}
		}
		n.Layer, n.Path = s.layer, s.path
		d := &Doc{
			ID: n.ID, Title: n.Title, Type: n.Type, Layer: s.layer, Rel: n.Rel, Path: s.path,
			Aliases: n.Aliases, Triggers: n.Triggers, Relations: n.Relations,
			Enforcement: n.Enforcement, Sensitivity: n.Sensitivity,
			Links: n.Links(), Session: n.SourceSession, When: n.When, Sources: n.Sources,
			Size: s.size, MTime: s.mtime, NoteProblems: n.Problems,
			Suspicious: note.Suspicious(n.Title + "\n" + n.Body),
			TextFile:   textFile, Unread: unread, Waiting: waiting,
			note: n,
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
	ix.link(today, kb)
	for _, d := range ix.Docs {
		if d.Unread != "" {
			d.Active, d.Reason = false, d.Unread
		}
	}

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

func (ix *part) save(dir, metaPath string, old *part) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ix.PostFile = fmt.Sprintf("index-%s-v%d-%d.post", ix.Key, formatVersion, time.Now().UnixNano())
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
	stale, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("index-%s-v%d-*.post", ix.Key, formatVersion)))
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
func (ix *part) link(today string, kb bool) {
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
	if kb {
		return // reference material never changes what holds in memory
	}
	for _, d := range ix.Docs {
		if d.note.Status == "draft" || d.Layer == "kb" {
			continue
		}
		for _, r := range d.Relations {
			t := lookupIn(ix.Docs, r.Target)
			switch {
			case t == nil:
				d.Problems = append(d.Problems, fmt.Sprintf("%s 指向的 %q 找不到", r.Op, r.Target))
				continue
			case t == d:
				d.Problems = append(d.Problems, fmt.Sprintf("%s 指向了自己", r.Op))
				continue
			case t.Layer == "kb":
				d.Problems = append(d.Problems, fmt.Sprintf("%s 指向的「%s」是知识库里的资料，不是记忆", r.Op, t.Title))
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
	ix.findCycles()
}

// findCycles reports notes whose @supersedes / @refines chain comes back to
// itself: such a chain has no current version.
func (ix *part) findCycles() {
	next := map[*Doc][]*Doc{}
	for _, d := range ix.Docs {
		for _, r := range d.Relations {
			if r.Op != "@supersedes" && r.Op != "@refines" {
				continue
			}
			if t := lookupIn(ix.Docs, r.Target); t != nil && t != d {
				next[d] = append(next[d], t)
			}
		}
	}
	const (
		white = iota
		grey
		black
	)
	color := map[*Doc]int{}
	var visit func(d *Doc, path []*Doc)
	visit = func(d *Doc, path []*Doc) {
		color[d] = grey
		path = append(path, d)
		for _, t := range next[d] {
			switch color[t] {
			case grey:
				var names []string
				start := 0
				for i, p := range path {
					if p == t {
						start = i
					}
				}
				for _, p := range path[start:] {
					names = append(names, p.Title)
				}
				t.Problems = append(t.Problems, "取代/细化关系成环："+strings.Join(names, " → ")+" → "+t.Title)
			case white:
				visit(t, path)
			}
		}
		color[d] = black
	}
	for _, d := range ix.Docs {
		if color[d] == white {
			visit(d, nil)
		}
	}
}

// Note returns the parsed note, reading the file on first use. If the file
// is gone it returns a stub built from the cached fields.
func (d *Doc) Note() *note.Note {
	d.once.Do(func() {
		if d.TextFile != "" {
			n := note.Parse("", d.Rel)
			n.ID, n.Title, n.Type = d.ID, d.Title, d.Type
			if data, err := os.ReadFile(d.TextFile); err == nil {
				if _, body, ok := strings.Cut(string(data), "\n"); ok {
					n.Body = body
				}
			}
			n.Layer, n.Path = d.Layer, d.Path
			d.note = n
			return
		}
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
func (ix *Index) Lookup(ref string) *Doc { return lookupIn(ix.Docs, ref) }

func lookupIn(docs []*Doc, ref string) *Doc {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	for _, d := range docs {
		if d.ID == ref {
			return d
		}
	}
	clean := strings.TrimSuffix(filepath.ToSlash(ref), ".md")
	for _, d := range docs {
		rel := strings.TrimSuffix(d.Rel, ".md")
		stem := rel[strings.LastIndex(rel, "/")+1:]
		if rel == clean || stem == clean || d.Path == ref {
			return d
		}
	}
	for _, d := range docs {
		if strings.EqualFold(d.Title, ref) {
			return d
		}
	}
	return nil
}

// DF is the number of notes in force that contain a term.
func (ix *Index) DF(t string) int {
	n := ix.mem.df(t)
	if ix.kb != nil {
		n += ix.kb.df(t)
	}
	return n
}

func (ix *part) df(t string) int {
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

// Postings lists the notes that contain a term (indexes into Docs).
func (ix *Index) Postings(t string) []Posting {
	out := ix.mem.postings(t)
	if ix.kb != nil {
		off := len(ix.mem.Docs)
		for _, p := range ix.kb.postings(t) {
			p.Doc += off
			out = append(out, p)
		}
	}
	return out
}

func (ix *part) postings(t string) []Posting {
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

// Neighbours are the notes in force that a note is tied to: by a relation
// either way, a [[wiki link]] either way, or the same source conversation.
// Superseded notes lead to the note that replaced them.
func (ix *Index) Neighbours(d *Doc) []*Doc {
	var out []*Doc
	seen := map[*Doc]bool{d: true}
	add := func(t *Doc) {
		for t != nil && !t.Active && t.SupersededBy != "" {
			t = ix.Lookup(t.SupersededBy)
		}
		if t != nil && !seen[t] && t.Active && t.Layer != "kb" {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, r := range d.Relations {
		add(ix.Lookup(r.Target))
	}
	for _, l := range d.Links {
		add(ix.Lookup(l))
	}
	for _, o := range ix.Docs {
		if o == d || o.Layer == "kb" {
			continue
		}
		for _, r := range o.Relations {
			if t := ix.Lookup(r.Target); t == d {
				add(o)
			}
		}
		for _, l := range o.Links {
			if t := ix.Lookup(l); t == d {
				add(o)
			}
		}
		if d.Session != "" && o.Session == d.Session {
			add(o)
		}
	}
	return out
}
