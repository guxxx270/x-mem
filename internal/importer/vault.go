// Package importer turns another memory tool's store into x-mem notes.
//
// 旧记忆库 keeps memory in an Obsidian vault. Pages there hold several facts
// each, and an alias table (90 系统文件/canonical_sources.psv) maps phrases to
// pages. The import keeps the vault untouched and writes new files only:
//
//   - the user-preference page is split, one rule per bullet, into rules/;
//   - every other page under 01 项目, 02 经验与方法, 03 资料与证据 and
//     04 已完成与复盘 becomes one note under notes/imported/, same path;
//   - time logs, session excerpts and 旧记忆库's own system pages are skipped,
//     and so are index pages and pages still equal to 旧记忆库's samples.
package importer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"x-mem/internal/note"
	"x-mem/internal/textutil"
)

// Planned is one file the import will write.
type Planned struct {
	From    string // page in the vault, relative
	To      string // file in the personal layer, relative
	ID      string
	Title   string
	Type    string
	Aliases []string
	Content string
}

// Skipped is a vault page the import leaves out, with the reason.
type Skipped struct {
	From   string
	Reason string
}

// Plan is what an import would do.
type Plan struct {
	Vault   string
	Notes   []Planned
	Skipped []Skipped
}

// Zones whose pages are memory. Everything else is skipped.
var memoryZones = []string{"01 项目", "02 经验与方法", "03 资料与证据", "04 已完成与复盘"}

var skipZones = map[string]string{
	"05 时间日志": "时间日志和会话摘录是原始记录，不当记忆导入（原库里照样能查）",
	"90 系统文件": "旧记忆库 自己的系统文件",
}

// 旧记忆库 reads a 旧记忆库 vault and plans the import.
func 旧记忆库(vault string) (*Plan, error) {
	st, err := os.Stat(vault)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("找不到 旧记忆库 库：%s", vault)
	}
	p := &Plan{Vault: vault}
	aliases := readAliasTable(filepath.Join(vault, "90 系统文件", "canonical_sources.psv"))
	ids := map[string]int{}
	files := map[string]int{}

	err = filepath.WalkDir(vault, func(abs string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := de.Name()
		if de.IsDir() {
			if abs != vault && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(vault, abs)
		rel = filepath.ToSlash(rel)
		zone := strings.SplitN(rel, "/", 2)[0]
		if why, ok := skipZones[zone]; ok {
			p.Skipped = append(p.Skipped, Skipped{rel, why})
			return nil
		}
		if !strings.Contains(rel, "/") {
			p.Skipped = append(p.Skipped, Skipped{rel, "库根目录的入口页，不是记忆"})
			return nil
		}
		if !contains(memoryZones, zone) {
			p.Skipped = append(p.Skipped, Skipped{rel, "不在 旧记忆库 的记忆分区里"})
			return nil
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			p.Skipped = append(p.Skipped, Skipped{rel, "读不了：" + err.Error()})
			return nil
		}
		sum := sha256.Sum256(src)
		if templateHashes[hex.EncodeToString(sum[:])] {
			p.Skipped = append(p.Skipped, Skipped{rel, "还是 旧记忆库 自带的示例页，没改过"})
			return nil
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		zoneName := strings.TrimLeft(zone, "0123456789 ")
		if strings.HasSuffix(stem, "入口") || path.Dir(rel) == zone && stem == zoneName {
			p.Skipped = append(p.Skipped, Skipped{rel, "目录页，不是记忆"})
			return nil
		}
		page := readPage(string(src), rel)
		if isPreferencePage(page) {
			for _, n := range splitPreferences(page, aliases[rel]) {
				n.ID = unique(ids, n.ID)
				n.To = uniqueFile(files, "rules/imported/"+fileName(n.Title))
				n.Content = render(n, page)
				p.Notes = append(p.Notes, n)
			}
			return nil
		}
		n := Planned{
			From:    rel,
			ID:      unique(ids, "kr-"+slug(stem)),
			Title:   page.title,
			Type:    pageType(page, zone),
			Aliases: pageAliases(page, aliases[rel]),
		}
		n.To = uniqueFile(files, "notes/imported/"+rel)
		n.Content = render(n, page)
		p.Notes = append(p.Notes, n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(p.Skipped, func(i, j int) bool { return p.Skipped[i].From < p.Skipped[j].From })
	return p, nil
}

// Write puts the planned files into the personal layer. Files that already
// exist are left alone unless force is set, so running the import twice is
// harmless.
func (p *Plan) Write(personal string, force bool) (written, kept int, err error) {
	for _, n := range p.Notes {
		dst := filepath.Join(personal, filepath.FromSlash(n.To))
		if _, err := os.Stat(dst); err == nil && !force {
			kept++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return written, kept, err
		}
		if err := os.WriteFile(dst, []byte(n.Content), 0o644); err != nil {
			return written, kept, err
		}
		written++
	}
	return written, kept, nil
}

type page struct {
	rel, title, typ, status, how, validFrom, source string
	triggers                                        []string
	body                                            string // without the H1 title line
}

func readPage(src, rel string) page {
	fm, body, _ := note.Frontmatter(src)
	pg := page{
		rel:       rel,
		title:     strings.TrimSpace(note.String(fm["title"])),
		typ:       strings.ToLower(strings.TrimSpace(note.String(fm["type"]))),
		status:    strings.ToLower(strings.TrimSpace(note.String(fm["status"]))),
		how:       strings.TrimSpace(note.String(fm["how"])),
		validFrom: strings.TrimSpace(note.String(fm["valid_from"])),
		source:    strings.TrimSpace(note.String(fm["source"])),
	}
	for _, t := range strings.FieldsFunc(note.String(fm["triggers"]), func(r rune) bool { return r == ';' || r == '；' }) {
		if t = strings.TrimSpace(t); t != "" {
			pg.triggers = append(pg.triggers, t)
		}
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n")), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		if pg.title == "" {
			pg.title = strings.TrimSpace(lines[0][2:])
		}
		lines = lines[1:]
	}
	pg.body = strings.TrimSpace(strings.Join(lines, "\n"))
	if pg.title == "" {
		pg.title = strings.TrimSuffix(filepath.Base(rel), ".md")
	}
	return pg
}

func isPreferencePage(pg page) bool {
	return strings.Contains(pg.title, "偏好") && strings.Contains(pg.rel, "/Agent/")
}

func pageType(pg page, zone string) string {
	switch {
	case zone == "01 项目":
		return "project"
	case pg.typ == "method" || zone == "02 经验与方法":
		return "howto"
	}
	return "fact"
}

// status maps 旧记忆库's states onto x-mem's. 旧记忆库's "provisional" pages
// may be cited but not enforced, which is what an active non-rule note is.
func status(s string) string {
	switch s {
	case "superseded", "deprecated":
		return s
	}
	return "active"
}

// aliasRow is one line of 旧记忆库's alias table.
type aliasRow struct {
	aliases []string
	anchor  string
}

func readAliasTable(path string) map[string][]aliasRow {
	out := map[string][]aliasRow{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		row := aliasRow{}
		for _, a := range strings.Split(parts[1], ";") {
			if a = strings.TrimSpace(a); a != "" {
				row.aliases = append(row.aliases, a)
			}
		}
		if len(parts) > 3 {
			row.anchor = strings.TrimSpace(parts[3])
		}
		rel := filepath.ToSlash(strings.TrimSpace(parts[2]))
		out[rel] = append(out[rel], row)
	}
	return out
}

// pageAliases merges the alias table rows and 旧记忆库's question triggers.
// Pages 旧记忆库 distilled from Codex sessions got machine-made alias lists
// (file names, error codes, "HTTP 200"); as direct-hit phrases those fire on
// unrelated questions, so only their longer Chinese aliases (five characters or more) are kept. The
// body still carries every keyword for ordinary search.
func pageAliases(pg page, rows []aliasRow) []string {
	var out []string
	for _, r := range rows {
		for _, a := range r.aliases {
			if pg.source == "codex-memory" && textutil.CJKLen(a) < 5 {
				continue
			}
			out = append(out, a)
		}
	}
	return dedupe(append(out, pg.triggers...))
}

var bulletRe = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
var labelRe = regexp.MustCompile(`^\*\*(.+?)\*\*\s*[：:]\s*(.*)$`)

// splitPreferences turns each top-level bullet into one rule. A bullet that
// is only a label over sub-bullets ("知识库整理习惯：") gives one rule per
// sub-bullet instead.
func splitPreferences(pg page, rows []aliasRow) []Planned {
	type item struct {
		label, text string
		subs        []string
	}
	var items []*item
	for _, line := range strings.Split(pg.body, "\n") {
		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			if t := strings.TrimSpace(line); t != "" && len(items) > 0 {
				last := items[len(items)-1]
				last.text = strings.TrimSpace(last.text + " " + t)
			}
			continue
		}
		indent, text := len(strings.ReplaceAll(m[1], "\t", "    ")), strings.TrimSpace(m[2])
		if indent >= 2 && len(items) > 0 {
			items[len(items)-1].subs = append(items[len(items)-1].subs, text)
			continue
		}
		it := &item{text: text}
		if lm := labelRe.FindStringSubmatch(text); lm != nil {
			it.label, it.text = strings.TrimSpace(lm[1]), strings.TrimSpace(lm[2])
		}
		items = append(items, it)
	}
	var out []Planned
	add := func(title, body string) {
		n := Planned{From: pg.rel, ID: fmt.Sprintf("kr-pref-%d", len(out)+1), Title: title, Type: "preference"}
		text := norm(title + body)
		for _, r := range rows {
			if r.anchor == "" || !strings.Contains(text, norm(r.anchor)) {
				continue
			}
			for _, a := range r.aliases {
				// Words for the whole page ("用户偏好") would make every
				// preference answer to them; keep only this rule's own.
				if !strings.Contains(norm(pg.title), norm(a)) {
					n.Aliases = append(n.Aliases, a)
				}
			}
		}
		n.Aliases = dedupe(n.Aliases)
		n.Content = body
		out = append(out, n)
	}
	for _, it := range items {
		switch {
		case it.text == "" && len(it.subs) > 0:
			for _, s := range it.subs {
				add(titleFrom(s), s)
			}
		case it.label != "":
			body := it.text
			for _, s := range it.subs {
				body += "\n- " + s
			}
			add(it.label, body)
		default:
			add(titleFrom(it.text), it.text)
		}
	}
	return out
}

var parenRe = regexp.MustCompile(`（[^）]*）|\([^)]*\)`)

// titleFrom makes a short title from a rule sentence: its first clause,
// without asides in brackets or Markdown marks.
func titleFrom(s string) string {
	s = strings.NewReplacer("**", "", "`", "").Replace(s)
	s = parenRe.ReplaceAllString(s, "")
	if i := strings.IndexAny(s, "，,；;。"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > 24 {
		s = string(rs[:24])
	}
	return s
}

func render(n Planned, pg page) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", n.ID)
	fmt.Fprintf(&b, "title: %s\n", quote(n.Title))
	fmt.Fprintf(&b, "type: %s\n", n.Type)
	fmt.Fprintf(&b, "status: %s\n", status(pg.status))
	if len(n.Aliases) > 0 {
		b.WriteString("aliases:\n")
		for _, a := range n.Aliases {
			fmt.Fprintf(&b, "  - %s\n", quote(a))
		}
	}
	if pg.validFrom != "" {
		fmt.Fprintf(&b, "valid_from: %s\n", quote(pg.validFrom))
	}
	fmt.Fprintf(&b, "imported_from: %s\n", quote("obsidian:"+pg.rel))
	b.WriteString("---\n")
	body := n.Content
	if body == "" { // a whole page
		body = pg.body
		if pg.how != "" && !strings.Contains(firstLines(body, 3), pg.how) {
			body = pg.how + "\n\n" + body
		}
	}
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func quote(s string) string {
	if !strings.Contains(s, `"`) {
		return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// norm keeps letters and digits only, lower-cased, for loose matching.
func norm(s string) string {
	var b strings.Builder
	for _, r := range textutil.Normalize(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]+`)

func fileName(title string) string {
	s := strings.TrimSpace(unsafeName.ReplaceAllString(title, "-"))
	if s == "" {
		s = "未命名"
	}
	return s + ".md"
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if rs := []rune(out); len(rs) > 48 {
		out = strings.TrimRight(string(rs[:48]), "-")
	}
	return out
}

func unique(seen map[string]int, id string) string {
	seen[id]++
	if seen[id] == 1 {
		return id
	}
	return fmt.Sprintf("%s-%d", id, seen[id])
}

func uniqueFile(seen map[string]int, rel string) string {
	seen[rel]++
	if seen[rel] == 1 {
		return rel
	}
	ext := filepath.Ext(rel)
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(rel, ext), seen[rel], ext)
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		k := norm(x)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
