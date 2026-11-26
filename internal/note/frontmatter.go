package note

import (
	"regexp"
	"strings"
)

// This file reads the YAML subset x-mem notes use: scalars, quoted strings,
// inline lists ([a, b]), block lists, maps nested under a key, and lists of
// maps (relations). Anything fancier is skipped rather than guessed at.
// x-mem never rewrites a frontmatter block wholesale, so unknown keys a
// person adds by hand survive untouched.

type yline struct {
	indent int
	text   string
}

var keyRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_\-]*):(\s|$)`)

func splitFrontmatter(src string) (fm string, body string, ok bool) {
	s := strings.ReplaceAll(src, "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---\n") {
		return "", s, false
	}
	rest := s[4:]
	for _, end := range []string{"\n---\n", "\n...\n"} {
		if i := strings.Index(rest, end); i >= 0 {
			return rest[:i], rest[i+len(end):], true
		}
	}
	for _, end := range []string{"\n---", "\n..."} {
		if strings.HasSuffix(rest, end) {
			return rest[:len(rest)-len(end)], "", true
		}
	}
	if strings.HasPrefix(rest, "---\n") { // empty frontmatter
		return "", rest[4:], true
	}
	return "", s, false
}

func parseYAML(src string) map[string]any {
	var ls []yline
	for _, raw := range strings.Split(src, "\n") {
		t := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(t, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		ls = append(ls, yline{indent: len(t) - len(trimmed), text: trimmed})
	}
	m, _ := parseMap(ls, 0, 0)
	return m
}

func isItem(s string) bool { return s == "-" || strings.HasPrefix(s, "- ") }

func parseMap(ls []yline, i, indent int) (map[string]any, int) {
	m := map[string]any{}
	for i < len(ls) && ls[i].indent == indent && !isItem(ls[i].text) {
		k := keyRe.FindStringSubmatch(ls[i].text)
		if k == nil {
			i++
			continue
		}
		key := k[1]
		rest := strings.TrimSpace(ls[i].text[len(key)+1:])
		i++
		switch {
		case rest != "":
			m[key] = scalarOrInline(rest)
		case i < len(ls) && ls[i].indent > indent:
			if isItem(ls[i].text) {
				m[key], i = parseList(ls, i, ls[i].indent)
			} else {
				m[key], i = parseMap(ls, i, ls[i].indent)
			}
		case i < len(ls) && ls[i].indent == indent && isItem(ls[i].text):
			m[key], i = parseList(ls, i, indent)
		default:
			m[key] = ""
		}
	}
	return m, i
}

func parseList(ls []yline, i, indent int) ([]any, int) {
	var out []any
	for i < len(ls) && ls[i].indent == indent && isItem(ls[i].text) {
		item := strings.TrimSpace(strings.TrimPrefix(ls[i].text, "-"))
		i++
		switch {
		case item == "":
			if i < len(ls) && ls[i].indent > indent {
				var v any
				if isItem(ls[i].text) {
					v, i = parseList(ls, i, ls[i].indent)
				} else {
					v, i = parseMap(ls, i, ls[i].indent)
				}
				out = append(out, v)
			}
		case keyRe.MatchString(item):
			k := keyRe.FindStringSubmatch(item)
			sub := map[string]any{}
			if rest := strings.TrimSpace(item[len(k[1])+1:]); rest != "" {
				sub[k[1]] = scalarOrInline(rest)
			} else {
				sub[k[1]] = ""
			}
			if i < len(ls) && ls[i].indent > indent && !isItem(ls[i].text) {
				var more map[string]any
				more, i = parseMap(ls, i, ls[i].indent)
				for kk, vv := range more {
					sub[kk] = vv
				}
			}
			out = append(out, sub)
		default:
			out = append(out, scalarOrInline(item))
		}
	}
	return out, i
}

func scalarOrInline(s string) any {
	s = stripComment(strings.TrimSpace(s))
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		var out []any
		for _, part := range splitInline(s[1 : len(s)-1]) {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, unquote(p))
			}
		}
		return out
	}
	return unquote(s)
}

// stripComment removes a trailing " # comment" that is outside quotes.
func stripComment(s string) string {
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func splitInline(s string) []string {
	var out []string
	var quote rune
	start := 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ',':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t")
		return r.Replace(inner)
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return ""
	}
}

func strList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s := strings.TrimSpace(str(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	}
	return nil
}

// Frontmatter splits a Markdown file into its frontmatter fields and body.
// ok is false when the file has no frontmatter block.
func Frontmatter(src string) (fields map[string]any, body string, ok bool) {
	fm, body, ok := splitFrontmatter(src)
	if !ok {
		return map[string]any{}, body, false
	}
	return parseYAML(fm), body, true
}

// String returns a frontmatter field as text ("" when absent or not text).
func String(v any) string { return str(v) }
