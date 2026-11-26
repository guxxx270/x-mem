package note

import (
	"fmt"
	"strings"
)

// Draft is a new note x-mem writes itself.
type Draft struct {
	ID, Title, Type string
	Aliases         []string
	ValidFrom       string
	Relations       []Relation
	Quote, Session  string
	At              string
	Triggers        []string
	Enforcement     string
	Sensitivity     string
	Body            string
}

// Render writes a draft as a Markdown note with frontmatter.
func (d Draft) Render() string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", d.ID)
	fmt.Fprintf(&b, "title: %s\n", Quote(d.Title))
	fmt.Fprintf(&b, "type: %s\n", d.Type)
	b.WriteString("status: active\n")
	writeList(&b, "aliases", d.Aliases)
	if d.ValidFrom != "" {
		fmt.Fprintf(&b, "valid_from: %s\n", d.ValidFrom)
	}
	if len(d.Relations) > 0 {
		b.WriteString("relations:\n")
		for _, r := range d.Relations {
			fmt.Fprintf(&b, "  - op: %s\n    target: %s\n", Quote(r.Op), Quote(r.Target))
			if r.Line != "" {
				fmt.Fprintf(&b, "    line: %s\n", Quote(r.Line))
			}
		}
	}
	if d.Quote != "" || d.Session != "" {
		b.WriteString("source:\n")
		if d.Quote != "" {
			fmt.Fprintf(&b, "  quote: %s\n", Quote(d.Quote))
		}
		if d.Session != "" {
			fmt.Fprintf(&b, "  session: %s\n", Quote(d.Session))
		}
		if d.At != "" {
			fmt.Fprintf(&b, "  at: %s\n", d.At)
		}
	}
	writeList(&b, "triggers", d.Triggers)
	if d.Enforcement != "" {
		fmt.Fprintf(&b, "enforcement: %s\n", d.Enforcement)
	}
	if d.Sensitivity != "" {
		fmt.Fprintf(&b, "sensitivity: %s\n", d.Sensitivity)
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(d.Body))
	b.WriteString("\n")
	return b.String()
}

func writeList(b *strings.Builder, key string, xs []string) {
	if len(xs) == 0 {
		return
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = Quote(x)
	}
	fmt.Fprintf(b, "%s: [%s]\n", key, strings.Join(parts, ", "))
}

// Quote writes a YAML scalar the frontmatter reader reads back unchanged.
func Quote(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if !strings.Contains(s, `"`) {
		return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// SetStatus changes the top-level status field of a note's source and
// returns the new source and the old value ("" when there was none). Every
// other byte of the file stays as it was.
func SetStatus(src, status string) (string, string) {
	nl := "\n"
	if strings.Contains(src, "\r\n") {
		nl = "\r\n"
	}
	bom := ""
	if strings.HasPrefix(src, "\ufeff") {
		bom, src = "\ufeff", strings.TrimPrefix(src, "\ufeff")
	}
	lines := strings.Split(src, nl)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return bom + "---" + nl + "status: " + status + nl + "---" + nl + src, ""
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); t == "---" || t == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return bom + src, ""
	}
	for i := 1; i < end; i++ {
		if strings.HasPrefix(lines[i], "status:") {
			old := strings.TrimSpace(strings.TrimPrefix(lines[i], "status:"))
			old = strings.Trim(stripComment(old), `"'`)
			lines[i] = "status: " + status
			return bom + strings.Join(lines, nl), old
		}
	}
	out := append([]string{}, lines[:end]...)
	out = append(out, "status: "+status)
	out = append(out, lines[end:]...)
	return bom + strings.Join(out, nl), ""
}

// RemoveStatus drops the top-level status field (used to undo a SetStatus
// on a note that had none).
func RemoveStatus(src string) string {
	nl := "\n"
	if strings.Contains(src, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(src, nl)
	if len(lines) == 0 || strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) != "---" {
		return src
	}
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); t == "---" || t == "..." {
			break
		}
		if strings.HasPrefix(lines[i], "status:") {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), nl)
		}
	}
	return src
}

// StatusOf reads the top-level status field from a note's source.
func StatusOf(src string) string {
	fm, _, ok := Frontmatter(src)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(String(fm["status"])))
}
