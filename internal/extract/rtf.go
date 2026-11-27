package extract

import (
	"strconv"
	"strings"
)

// RTF: text, paragraphs, tables, \uN Unicode and \'hh bytes in the
// document's code page (GBK when it says 936 or a font is GB2312).

var rtfSkip = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true, "object": true,
	"themedata": true, "colorschememapping": true, "latentstyles": true, "datastore": true, "xmlnstbl": true,
	"listtable": true, "listoverridetable": true, "rsidtbl": true, "generator": true, "mmathPr": true,
	"filetbl": true, "revtbl": true, "pgdsctbl": true, "fldinst": true, "datafield": true, "objdata": true,
	"shppict": true, "nonshppict": true, "blipuid": true, "wgrffmtfilter": true, "pnseclvl": true,
}

var rtfChars = map[string]string{
	"par": "\n", "line": "\n", "sect": "\n", "page": "\n", "row": "\n", "tab": "\t", "cell": "\t",
	"emdash": "—", "endash": "–", "bullet": "•", "lquote": "‘", "rquote": "’", "ldblquote": "“", "rdblquote": "”",
	"emspace": " ", "enspace": " ", "qmspace": " ",
}

type rtfState struct {
	skip  bool
	uc    int
	font  int
	fonts bool // inside the font table
}

func rtf(_ string, data []byte) (string, error) {
	src := string(data)
	var out strings.Builder
	var pending []byte // \'hh bytes waiting to be decoded together
	codepage := 1252
	fontCharset := map[int]int{}
	st := rtfState{uc: 1}
	var stack []rtfState
	curFontDef := -1

	flush := func() {
		if len(pending) == 0 {
			return
		}
		cs := fontCharset[st.font]
		if codepage == 936 || cs == 134 {
			out.WriteString(decodeGBK(pending))
		} else {
			out.WriteString(decodeCP1252(pending))
		}
		pending = pending[:0]
	}
	skipChars := 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch c {
		case '{':
			flush()
			stack = append(stack, st)
		case '}':
			flush()
			if len(stack) > 0 {
				st = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		case '\\':
			if i+1 >= len(src) {
				break
			}
			n := src[i+1]
			switch {
			case n == '\'' && i+3 < len(src):
				v, err := strconv.ParseUint(src[i+2:i+4], 16, 8)
				i += 3
				if skipChars > 0 {
					skipChars--
					continue
				}
				if err == nil && !st.skip {
					pending = append(pending, byte(v))
				}
			case n == '\\' || n == '{' || n == '}':
				flush()
				i++
				if !st.skip && skipChars == 0 {
					out.WriteByte(n)
				} else if skipChars > 0 {
					skipChars--
				}
			case n == '~':
				i++
				if !st.skip {
					out.WriteByte(' ')
				}
			case n == '_':
				i++
				if !st.skip {
					out.WriteByte('-')
				}
			case n == '*':
				i++
				st.skip = true
			case n == '\n' || n == '\r':
				flush()
				i++
				if !st.skip {
					out.WriteByte('\n')
				}
			case isLetter(n):
				flush()
				j := i + 1
				for j < len(src) && isLetter(src[j]) {
					j++
				}
				word := src[i+1 : j]
				k := j
				if k < len(src) && (src[k] == '-' || isDigit(src[k])) {
					k++
					for k < len(src) && isDigit(src[k]) {
						k++
					}
				}
				param, hasParam := 0, k > j
				if hasParam {
					param, _ = strconv.Atoi(src[j:k])
				}
				if k < len(src) && src[k] == ' ' {
					k++
				}
				i = k - 1
				switch {
				case word == "u" && hasParam:
					if param < 0 {
						param += 65536
					}
					if !st.skip {
						out.WriteRune(rune(param))
					}
					skipChars = st.uc
				case word == "uc" && hasParam:
					st.uc = param
				case word == "ansicpg" && hasParam:
					codepage = param
				case word == "fonttbl":
					st.fonts, st.skip = true, true
				case word == "f" && hasParam:
					if st.fonts {
						curFontDef = param
					} else {
						st.font = param
					}
				case word == "fcharset" && hasParam && st.fonts && curFontDef >= 0:
					fontCharset[curFontDef] = param
				case rtfSkip[word]:
					st.skip = true
				default:
					if s, ok := rtfChars[word]; ok && !st.skip {
						out.WriteString(s)
					}
				}
			default:
				i++
			}
		case '\r', '\n':
		default:
			flush()
			if skipChars > 0 {
				skipChars--
				continue
			}
			if !st.skip {
				out.WriteByte(c)
			}
		}
	}
	flush()
	return out.String(), nil
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
