// Package guard matches rule triggers against the tool call an agent is
// about to make. Triggers use saga's form, Tool(pattern): "Bash(git push*)",
// "Edit(*.sql)", "WebFetch(https://internal.*)". A trigger without brackets
// matches every call of that tool.
package guard

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var triggerRe = regexp.MustCompile(`^\s*([A-Za-z0-9_*?\-]+)\s*(?:\((.*)\))?\s*$`)

// ParseTrigger splits "Tool(pattern)" into its parts.
func ParseTrigger(t string) (tool, pattern string, err error) {
	m := triggerRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", fmt.Errorf("要写成 工具名(通配符)，例如 Bash(git push*)")
	}
	return m[1], strings.TrimSpace(m[2]), nil
}

// Subjects are the strings a trigger's pattern is matched against for one
// tool call. A Bash command is split at && ; | and newlines, so
// "make && git push" still matches "git push*".
func Subjects(tool string, input json.RawMessage) []string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		s, _ := in[k].(string)
		return s
	}
	switch tool {
	case "Bash":
		cmd := str("command")
		var out []string
		for _, part := range splitCommand(cmd) {
			out = append(out, part)
		}
		if len(out) == 0 {
			out = append(out, cmd)
		}
		return out
	case "Edit", "Write", "MultiEdit", "Read":
		return []string{str("file_path")}
	case "NotebookEdit":
		return []string{str("notebook_path")}
	case "WebFetch":
		return []string{str("url")}
	case "WebSearch":
		return []string{str("query")}
	case "Glob", "Grep":
		return []string{str("pattern")}
	}
	b, _ := json.Marshal(in)
	return []string{string(b)}
}

var splitRe = regexp.MustCompile(`&&|\|\||;|\||\n`)
var envRe = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+`)

func splitCommand(cmd string) []string {
	var out []string
	for _, p := range splitRe.Split(cmd, -1) {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "(")
		p = envRe.ReplaceAllString(p, "")
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Match reports whether a trigger applies to this tool call.
func Match(trigger, tool string, input json.RawMessage) bool {
	t, pattern, err := ParseTrigger(trigger)
	if err != nil || !glob(t, tool) {
		return false
	}
	if pattern == "" || pattern == "*" {
		return true
	}
	for _, s := range Subjects(tool, input) {
		if glob(pattern, s) {
			return true
		}
	}
	return false
}

// glob matches * (any run, slashes and spaces included) and ? (one rune).
func glob(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
