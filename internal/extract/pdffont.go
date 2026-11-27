package extract

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Fonts turn the bytes of a shown string into Unicode text and widths.

type pdfFont struct {
	composite bool
	spaces    []codeRange // code lengths, from the ToUnicode or encoding CMap
	toUni     map[uint32]string
	ranges    []uniRange
	simple    [256]rune // simple fonts: code → rune from the encoding
	builtin   string    // composite fonts without ToUnicode: "utf16", "gbk", "utf8" or ""
	widths    map[uint32]float64
	dw        float64 // default width (1/1000 em)
}

type codeRange struct {
	n      int
	lo, hi uint32
}

type uniRange struct {
	lo, hi uint32
	base   []uint16 // destination of lo, as UTF-16
	list   []string // or one destination per code
}

type glyph struct {
	text  string
	code  uint32
	width float64
	space bool // a single-byte space: word spacing applies
	known bool // the font says what character it is
}

func (f *pdfFile) loadFont(v any, cache map[any]*pdfFont) *pdfFont {
	key := v
	if r, ok := v.(pdfRef); ok {
		key = r
	} else {
		key = nil
	}
	if key != nil {
		if ft, ok := cache[key]; ok {
			return ft
		}
	}
	d := f.dict(v)
	ft := &pdfFont{dw: 1000, widths: map[uint32]float64{}}
	if d == nil {
		return ft
	}
	sub := name(d["Subtype"])
	if sub == "Type0" {
		ft.composite = true
		enc := f.resolve(d["Encoding"])
		switch e := enc.(type) {
		case pdfName:
			n := string(e)
			switch {
			case strings.Contains(n, "UCS2") || strings.Contains(n, "UTF16"):
				ft.builtin = "utf16"
				ft.spaces = []codeRange{{2, 0, 0xFFFF}}
			case strings.Contains(n, "UTF8"):
				ft.builtin = "utf8"
			case strings.HasPrefix(n, "GB") && (strings.Contains(n, "EUC") || strings.HasPrefix(n, "GBK")):
				ft.builtin = "gbk"
				ft.spaces = []codeRange{{1, 0, 0x80}, {2, 0x8140, 0xFEFE}}
			default: // Identity-H / -V and others: two bytes per code
				ft.spaces = []codeRange{{2, 0, 0xFFFF}}
			}
		case *pdfStream:
			if data, err := f.decode(e); err == nil {
				ft.spaces = parseCMap(data, nil).spaces
			}
		}
		if len(ft.spaces) == 0 {
			ft.spaces = []codeRange{{2, 0, 0xFFFF}}
		}
		if desc := f.array(d["DescendantFonts"]); len(desc) > 0 {
			dd := f.dict(desc[0])
			if dd != nil {
				if w, ok := numf(f.resolve(dd["DW"])); ok {
					ft.dw = w
				}
				ft.compositeWidths(f, f.array(dd["W"]))
			}
		}
	} else {
		ft.simpleEncoding(f, f.resolve(d["Encoding"]))
		first := num(f.resolve(d["FirstChar"]))
		for i, w := range f.array(d["Widths"]) {
			if x, ok := numf(f.resolve(w)); ok {
				ft.widths[uint32(first+i)] = x
			}
		}
		ft.dw = 500
		if fd := f.dict(d["FontDescriptor"]); fd != nil {
			if w, ok := numf(f.resolve(fd["MissingWidth"])); ok && w > 0 {
				ft.dw = w
			}
		}
	}
	if s := f.stream(d["ToUnicode"]); s != nil {
		if data, err := f.decode(s); err == nil {
			cm := parseCMap(data, ft)
			if ft.composite && len(cm.spaces) > 0 {
				ft.spaces = cm.spaces
			}
		}
	}
	if key != nil {
		cache[key] = ft
	}
	return ft
}

func (ft *pdfFont) compositeWidths(f *pdfFile, w []any) {
	for i := 0; i < len(w); {
		c1, ok := numf(f.resolve(w[i]))
		if !ok || i+1 >= len(w) {
			return
		}
		if arr, ok := f.resolve(w[i+1]).([]any); ok {
			for k, x := range arr {
				if v, ok := numf(f.resolve(x)); ok {
					ft.widths[uint32(c1)+uint32(k)] = v
				}
			}
			i += 2
			continue
		}
		c2, ok1 := numf(f.resolve(w[i+1]))
		if i+2 >= len(w) || !ok1 {
			return
		}
		v, _ := numf(f.resolve(w[i+2]))
		if c2-c1 < 65536 {
			for c := uint32(c1); c <= uint32(c2); c++ {
				ft.widths[c] = v
			}
		}
		i += 3
	}
}

func (ft *pdfFont) simpleEncoding(f *pdfFile, enc any) {
	base := "StandardEncoding"
	var diffs []any
	switch e := enc.(type) {
	case pdfName:
		base = string(e)
	case pdfDict:
		if b := name(e["BaseEncoding"]); b != "" {
			base = b
		}
		diffs = f.array(e["Differences"])
	}
	for c := 0; c < 256; c++ {
		switch {
		case base == "MacRomanEncoding" && c >= 128:
			ft.simple[c] = macRoman[c-128]
		case c >= 0x80 && c < 0xA0:
			ft.simple[c] = cp1252[c-0x80]
		case c >= 32:
			ft.simple[c] = rune(c)
		}
	}
	if base == "StandardEncoding" {
		ft.simple['\''] = '’'
		ft.simple['`'] = '‘'
	}
	code := 0
	for _, x := range diffs {
		switch v := f.resolve(x).(type) {
		case float64:
			code = int(v)
		case pdfName:
			if code >= 0 && code < 256 {
				ft.simple[code] = glyphRune(string(v))
			}
			code++
		}
	}
}

// decode splits a shown string into glyphs.
func (ft *pdfFont) decode(s []byte) []glyph {
	var out []glyph
	if !ft.composite {
		for _, b := range s {
			g := glyph{code: uint32(b), space: b == 32}
			if t, ok := ft.toUni[uint32(b)]; ok {
				g.text, g.known = t, true
			} else if t := ft.lookupRange(uint32(b)); t != "" {
				g.text, g.known = t, true
			} else if r := ft.simple[b]; r != 0 {
				g.text, g.known = string(r), true
			}
			g.width = ft.width(uint32(b))
			out = append(out, g)
		}
		return out
	}
	if ft.builtin == "utf8" && len(ft.toUni) == 0 && len(ft.ranges) == 0 {
		for _, r := range string(s) {
			out = append(out, glyph{text: string(r), code: uint32(r), width: ft.dw, known: r != utf8.RuneError})
		}
		return out
	}
	for i := 0; i < len(s); {
		n := ft.codeLen(s[i:])
		var code uint32
		for k := 0; k < n; k++ {
			code = code<<8 | uint32(s[i+k])
		}
		raw := s[i : i+n]
		i += n
		g := glyph{code: code, width: ft.width(code), space: n == 1 && code == 32}
		if t, ok := ft.toUni[code]; ok {
			g.text, g.known = t, true
		} else if t := ft.lookupRange(code); t != "" {
			g.text, g.known = t, true
		} else {
			switch ft.builtin {
			case "utf16":
				g.text, g.known = string(rune(code)), true
			case "gbk":
				g.text = decodeGBK(raw)
				g.known = g.text != ""
			}
		}
		out = append(out, g)
	}
	if ft.builtin == "utf16" { // join surrogate pairs
		for i := 0; i+1 < len(out); i++ {
			a, b := out[i].code, out[i+1].code
			if a >= 0xD800 && a < 0xDC00 && b >= 0xDC00 && b < 0xE000 {
				out[i].text = string(utf16.DecodeRune(rune(a), rune(b)))
				out = append(out[:i+1], out[i+2:]...)
			}
		}
	}
	return out
}

func (ft *pdfFont) codeLen(b []byte) int {
	for n := 1; n <= 4 && n <= len(b); n++ {
		var code uint32
		for k := 0; k < n; k++ {
			code = code<<8 | uint32(b[k])
		}
		for _, r := range ft.spaces {
			if r.n == n && code >= r.lo && code <= r.hi {
				return n
			}
		}
	}
	// No range matched: use the shortest length the font uses.
	n := 2
	for _, r := range ft.spaces {
		if r.n < n {
			n = r.n
		}
	}
	if n > len(b) {
		n = len(b)
	}
	if n < 1 {
		n = 1
	}
	return n
}

func (ft *pdfFont) width(code uint32) float64 {
	if w, ok := ft.widths[code]; ok {
		return w
	}
	return ft.dw
}

func (ft *pdfFont) lookupRange(code uint32) string {
	i := sort.Search(len(ft.ranges), func(i int) bool { return ft.ranges[i].hi >= code })
	for ; i < len(ft.ranges); i++ {
		r := ft.ranges[i]
		if r.lo > code {
			return ""
		}
		if code < r.lo || code > r.hi {
			continue
		}
		off := code - r.lo
		if r.list != nil {
			if int(off) < len(r.list) {
				return r.list[off]
			}
			return ""
		}
		u := append([]uint16(nil), r.base...)
		if len(u) == 0 {
			return ""
		}
		u[len(u)-1] += uint16(off)
		return string(utf16.Decode(u))
	}
	return ""
}

// parseCMap reads a CMap: its code space ranges and, when ft is given, its
// bfchar / bfrange mappings into ft.
type cmapResult struct{ spaces []codeRange }

func parseCMap(data []byte, ft *pdfFont) cmapResult {
	var res cmapResult
	l := &pdfLexer{b: data}
	var ops []any
	for {
		v, err := l.next()
		if err != nil {
			break
		}
		k, isKw := v.(pdfKeyword)
		if !isKw {
			ops = append(ops, v)
			continue
		}
		switch string(k) {
		case "endcodespacerange":
			for i := 0; i+1 < len(ops); i += 2 {
				lo, _ := ops[i].(pdfString)
				hi, _ := ops[i+1].(pdfString)
				if len(lo) > 0 && len(lo) == len(hi) && len(lo) <= 4 {
					res.spaces = append(res.spaces, codeRange{len(lo), beUint(lo), beUint(hi)})
				}
			}
		case "endbfchar":
			if ft == nil {
				break
			}
			if ft.toUni == nil {
				ft.toUni = map[uint32]string{}
			}
			for i := 0; i+1 < len(ops); i += 2 {
				src, _ := ops[i].(pdfString)
				switch dst := ops[i+1].(type) {
				case pdfString:
					ft.toUni[beUint(src)] = utf16BE(dst)
				case pdfName:
					ft.toUni[beUint(src)] = string(glyphRune(string(dst)))
				}
			}
		case "endbfrange":
			if ft == nil {
				break
			}
			for i := 0; i+2 < len(ops); i += 3 {
				lo, _ := ops[i].(pdfString)
				hi, _ := ops[i+1].(pdfString)
				r := uniRange{lo: beUint(lo), hi: beUint(hi)}
				if r.hi < r.lo || r.hi-r.lo > 1<<20 {
					continue
				}
				switch dst := ops[i+2].(type) {
				case pdfString:
					for k := 0; k+1 < len(dst); k += 2 {
						r.base = append(r.base, uint16(dst[k])<<8|uint16(dst[k+1]))
					}
				case []any:
					for _, x := range dst {
						s, _ := x.(pdfString)
						r.list = append(r.list, utf16BE(s))
					}
				}
				ft.ranges = append(ft.ranges, r)
			}
			sort.Slice(ft.ranges, func(a, b int) bool { return ft.ranges[a].lo < ft.ranges[b].lo })
		}
		if strings.HasPrefix(string(k), "end") || strings.HasPrefix(string(k), "begin") || k == "def" || k == "usecmap" {
			ops = ops[:0]
		}
	}
	return res
}

func beUint(b []byte) uint32 {
	var v uint32
	for _, c := range b {
		v = v<<8 | uint32(c)
	}
	return v
}

func utf16BE(b []byte) string {
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	return decodeUTF16(b, true)
}

// glyphRune maps a glyph name to its character: uniXXXX, uXXXX[XX] and the
// common names of the Adobe Glyph List.
func glyphRune(n string) rune {
	if r, ok := aglNames[n]; ok {
		return r
	}
	if strings.HasPrefix(n, "uni") && len(n) >= 7 {
		if v, err := strconv.ParseUint(n[3:7], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(n, "u") && len(n) >= 5 && len(n) <= 7 {
		if v, err := strconv.ParseUint(n[1:], 16, 32); err == nil {
			return rune(v)
		}
	}
	if len(n) == 1 {
		return rune(n[0])
	}
	if i := strings.IndexByte(n, '.'); i > 0 {
		return glyphRune(n[:i])
	}
	return 0
}

var aglNames = map[string]rune{
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#', "dollar": '$', "percent": '%', "ampersand": '&',
	"quotesingle": '\'', "quoteright": '’', "parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+', "comma": ',',
	"hyphen": '-', "minus": '−', "period": '.', "slash": '/', "zero": '0', "one": '1', "two": '2', "three": '3', "four": '4',
	"five": '5', "six": '6', "seven": '7', "eight": '8', "nine": '9', "colon": ':', "semicolon": ';', "less": '<',
	"equal": '=', "greater": '>', "question": '?', "at": '@', "bracketleft": '[', "backslash": '\\', "bracketright": ']',
	"asciicircum": '^', "underscore": '_', "grave": '`', "quoteleft": '‘', "braceleft": '{', "bar": '|', "braceright": '}',
	"asciitilde": '~', "bullet": '•', "endash": '–', "emdash": '—', "quotedblleft": '“', "quotedblright": '”',
	"quotesinglbase": '‚', "quotedblbase": '„', "ellipsis": '…', "fi": 'ﬁ', "fl": 'ﬂ', "ff": 'ﬀ', "ffi": 'ﬃ', "ffl": 'ﬄ',
	"dagger": '†', "daggerdbl": '‡', "trademark": '™', "copyright": '©', "registered": '®', "degree": '°',
	"section": '§', "paragraph": '¶', "periodcentered": '·', "multiply": '×', "divide": '÷', "plusminus": '±',
	"Euro": '€', "sterling": '£', "yen": '¥', "cent": '¢', "nbspace": ' ', "uni00A0": ' ', "guillemotleft": '«',
	"guillemotright": '»', "exclamdown": '¡', "questiondown": '¿', "dotlessi": 'ı', "germandbls": 'ß',
	"Adieresis": 'Ä', "Odieresis": 'Ö', "Udieresis": 'Ü', "adieresis": 'ä', "odieresis": 'ö', "udieresis": 'ü',
	"eacute": 'é', "egrave": 'è', "ecircumflex": 'ê', "aacute": 'á', "agrave": 'à', "acircumflex": 'â', "ccedilla": 'ç',
	"iacute": 'í', "oacute": 'ó', "uacute": 'ú', "ntilde": 'ñ', "Eacute": 'É', "space.alt": ' ', "arrowright": '→',
}

var macRoman = [128]rune{
	'Ä', 'Å', 'Ç', 'É', 'Ñ', 'Ö', 'Ü', 'á', 'à', 'â', 'ä', 'ã', 'å', 'ç', 'é', 'è',
	'ê', 'ë', 'í', 'ì', 'î', 'ï', 'ñ', 'ó', 'ò', 'ô', 'ö', 'õ', 'ú', 'ù', 'û', 'ü',
	'†', '°', '¢', '£', '§', '•', '¶', 'ß', '®', '©', '™', '´', '¨', '≠', 'Æ', 'Ø',
	'∞', '±', '≤', '≥', '¥', 'µ', '∂', '∑', '∏', 'π', '∫', 'ª', 'º', 'Ω', 'æ', 'ø',
	'¿', '¡', '¬', '√', 'ƒ', '≈', '∆', '«', '»', '…', ' ', 'À', 'Ã', 'Õ', 'Œ', 'œ',
	'–', '—', '“', '”', '‘', '’', '÷', '◊', 'ÿ', 'Ÿ', '⁄', '€', '‹', '›', 'ﬁ', 'ﬂ',
	'‡', '·', '‚', '„', '‰', 'Â', 'Ê', 'Á', 'Ë', 'È', 'Í', 'Î', 'Ï', 'Ì', 'Ó', 'Ô',
	'\uF8FF', 'Ò', 'Ú', 'Û', 'Ù', 'ı', 'ˆ', '˜', '¯', '˘', '˙', '˚', '¸', '˝', '˛', 'ˇ',
}
