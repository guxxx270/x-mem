// Package note reads one Markdown note: its frontmatter fields and body.
package note

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Relation is a typed link to another note, e.g. @supersedes.
type Relation struct {
	Op     string // one of KnownOps
	Target string // the other note's id, file name or title
	Line   string // optional: only this line of the target is affected
}

// KnownOps are the relation operators x-mem understands. Anything else is an
// error, never silently ignored (saga's lesson: a missing "@" did nothing).
var KnownOps = []string{"@supersedes", "@refines", "@conflicts_with", "@deprecated", "@relates_to"}

// Note is one Markdown file in a layer.
type Note struct {
	ID            string
	Title         string
	Type          string
	Status        string
	Aliases       []string
	ValidFrom     string
	ValidUntil    string
	Relations     []Relation
	Triggers      []string
	Enforcement   string
	Sensitivity   string
	SourceQuote   string
	SourceSession string
	When          string   // skill: the situation it is for
	Sources       []string // skill: ids of the notes it grew from
	Body          string

	Layer string // "personal" or "project"
	Rel   string // path relative to the layer root, with forward slashes
	Path  string // absolute path

	Problems []string // things doctor should report
}

var headingRe = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// Parse reads a note from its file contents. rel is the path relative to the
// layer root and is used for the fallback id and title.
func Parse(src, rel string) *Note {
	n := &Note{Rel: filepath.ToSlash(rel)}
	fm, body, has := splitFrontmatter(src)
	n.Body = strings.TrimSpace(body)
	stem := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	if has {
		m := parseYAML(fm)
		n.ID = strings.TrimSpace(str(m["id"]))
		n.Title = strings.TrimSpace(str(m["title"]))
		n.Type = strings.ToLower(strings.TrimSpace(str(m["type"])))
		n.Status = strings.ToLower(strings.TrimSpace(str(m["status"])))
		n.Aliases = strList(m["aliases"])
		n.ValidFrom = strings.TrimSpace(str(m["valid_from"]))
		n.ValidUntil = strings.TrimSpace(str(m["valid_until"]))
		n.Triggers = strList(m["triggers"])
		n.Enforcement = strings.ToLower(strings.TrimSpace(str(m["enforcement"])))
		n.Sensitivity = strings.ToLower(strings.TrimSpace(str(m["sensitivity"])))
		n.When = strings.TrimSpace(str(m["when"]))
		n.Sources = strList(m["sources"])
		if src, ok := m["source"].(map[string]any); ok {
			n.SourceQuote = strings.TrimSpace(str(src["quote"]))
			n.SourceSession = strings.TrimSpace(str(src["session"]))
		}
		if rels, ok := m["relations"].([]any); ok {
			for _, r := range rels {
				rm, ok := r.(map[string]any)
				if !ok {
					n.Problems = append(n.Problems, "relations 里有一项不是 op/target 的写法")
					continue
				}
				rel := Relation{
					Op:     strings.TrimSpace(str(rm["op"])),
					Target: strings.TrimSpace(str(rm["target"])),
					Line:   strings.TrimSpace(str(rm["line"])),
				}
				if !isKnownOp(rel.Op) {
					n.Problems = append(n.Problems, fmt.Sprintf("关系名 %q 不认识，只能用 %s", rel.Op, strings.Join(KnownOps, "、")))
					continue
				}
				if rel.Target == "" {
					n.Problems = append(n.Problems, fmt.Sprintf("关系 %s 没写 target", rel.Op))
					continue
				}
				n.Relations = append(n.Relations, rel)
			}
		} else if _, present := m["relations"]; present && str(m["relations"]) != "" {
			n.Problems = append(n.Problems, "relations 应该是一个列表")
		}
		if n.Enforcement != "" && n.Enforcement != "advise" && n.Enforcement != "block" {
			n.Problems = append(n.Problems, fmt.Sprintf("enforcement %q 只能是 advise 或 block", n.Enforcement))
		}
	}
	if n.Title == "" {
		if h := headingRe.FindStringSubmatch(n.Body); h != nil {
			n.Title = strings.TrimSpace(h[1])
		} else {
			n.Title = stem
		}
	}
	if n.ID == "" {
		n.ID = "file:" + strings.TrimSuffix(n.Rel, filepath.Ext(n.Rel))
	}
	if n.Status == "" {
		n.Status = "active"
	}
	if n.Type == "" {
		n.Type = "note"
	}
	return n
}

func isKnownOp(op string) bool {
	for _, k := range KnownOps {
		if op == k {
			return true
		}
	}
	return false
}

// Stem is the file name without directory or extension.
func (n *Note) Stem() string {
	return strings.TrimSuffix(filepath.Base(n.Rel), filepath.Ext(n.Rel))
}

// BodyLines returns the non-empty body lines, without the title heading.
func (n *Note) BodyLines() []string {
	var out []string
	for _, l := range strings.Split(n.Body, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if h := headingRe.FindStringSubmatch(t); h != nil && strings.TrimSpace(h[1]) == n.Title {
			continue
		}
		out = append(out, t)
	}
	return out
}

// IsRule reports whether the note is a rule or preference: these are listed
// at session start.
func (n *Note) IsRule() bool {
	return n.Type == "rule" || n.Type == "preference" || n.Type == "policy" || n.Type == "convention"
}

var injectionRe = regexp.MustCompile(`(?i)(ignore (all |any )?(previous|prior|above) (instructions|rules)|disregard (the )?(previous|above)|system prompt|you are now|忽略(之前|以上|上面|前面)的?(所有)?(指令|规则|要求)|无视(之前|以上)的?(指令|规则))`)

// Suspicious reports whether text reads like it tries to give orders to the
// agent instead of recording a fact.
func Suspicious(text string) bool { return injectionRe.MatchString(text) }

var wikiRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:[#|][^\]]*)?\]\]`)

// Links are the [[wiki links]] in the body, as written.
func (n *Note) Links() []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range wikiRe.FindAllStringSubmatch(n.Body, -1) {
		t := strings.TrimSpace(m[1])
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
