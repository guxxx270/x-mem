package extract

import (
	"html"
	"regexp"
	"strings"
)

var metaCharset = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?([A-Za-z0-9_\-]+)`)
var xmlEncoding = regexp.MustCompile(`(?i)<\?xml[^>]+encoding\s*=\s*["']([A-Za-z0-9_\-]+)`)

func htmlFile(_ string, data []byte) (string, error) { return htmlText(data), nil }

// htmlText reads the visible text of an HTML page: scripts, styles and
// comments dropped, block elements on their own lines, cells tab-separated.
func htmlText(data []byte) string {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	var src string
	if m := metaCharset.FindSubmatch(head); m != nil {
		src = decodeCharset(data, string(m[1]))
	} else if m := xmlEncoding.FindSubmatch(head); m != nil {
		src = decodeCharset(data, string(m[1]))
	} else {
		src = decodeText(data)
	}
	var b strings.Builder
	i := 0
	lower := strings.ToLower(src)
	for i < len(src) {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			b.WriteString(html.UnescapeString(src[i:]))
			break
		}
		b.WriteString(html.UnescapeString(src[i : i+lt]))
		i += lt
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		gt := strings.IndexByte(src[i:], '>')
		if gt < 0 {
			break
		}
		tag := strings.ToLower(src[i+1 : i+gt])
		i += gt + 1
		name := strings.TrimLeft(tag, "/")
		if sp := strings.IndexAny(name, " \t\n\r/"); sp >= 0 {
			name = name[:sp]
		}
		closing := strings.HasPrefix(tag, "/")
		switch name {
		case "script", "style", "noscript", "template", "svg", "head":
			if !closing {
				end := strings.Index(lower[i:], "</"+name)
				if end < 0 {
					i = len(src)
				} else {
					i += end
				}
			}
		case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article",
			"header", "footer", "blockquote", "pre", "table", "ul", "ol", "dt", "dd", "title", "hr", "figcaption":
			b.WriteByte('\n')
		case "td", "th":
			if closing {
				b.WriteByte('\t')
			}
		}
	}
	return collapseSpaces(b.String())
}

var spaceRun = regexp.MustCompile(`[ \t\x{00A0}]{2,}`)

func collapseSpaces(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(spaceRun.ReplaceAllStringFunc(l, func(m string) string {
			if strings.Contains(m, "\t") {
				return "\t"
			}
			return " "
		}))
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}
