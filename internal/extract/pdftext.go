package extract

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reading the text of each page: walk the page tree, run the content
// streams (and the forms they draw) and lay the glyphs out in lines.

type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

func (a matrix) mul(b matrix) matrix {
	return matrix{
		a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3],
		a[4]*b[0] + a[5]*b[2] + b[4], a[4]*b[1] + a[5]*b[3] + b[5],
	}
}

type gstate struct {
	ctm                        matrix
	font                       *pdfFont
	size, tc, tw, th, tl, rise float64
}

type pageWriter struct {
	actual        []*actualSpan // marked content with /ActualText, innermost last
	b             strings.Builder
	started       bool
	lastX, lastY  float64 // where the last glyph ended
	lastSize      float64
	lastRune      rune
	glyphs, known int
}

func isWide(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// span is the innermost marked-content sequence with actual text, if any.
func (w *pageWriter) span() *actualSpan {
	for i := len(w.actual) - 1; i >= 0; i-- {
		if w.actual[i] != nil {
			return w.actual[i]
		}
	}
	return nil
}

// textString decodes a PDF text string: UTF-16BE with a byte-order mark, or
// PDFDocEncoding (close enough to Latin-1).
func textString(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		return decodeUTF16(b[2:], true)
	}
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:])
	}
	return decodeCP1252(b)
}

// place writes a glyph's text at (x, y) in device space, adding a line
// break or a space when it does not follow on from the last one.
func (w *pageWriter) place(text string, x, y, endX, size float64) {
	if text == "" {
		return
	}
	first := []rune(text)[0]
	if w.started {
		dy := math.Abs(y - w.lastY)
		gap := x - w.lastX
		ref := math.Max(size, w.lastSize)
		if ref <= 0 {
			ref = 1
		}
		switch {
		case dy > ref*0.6:
			w.b.WriteByte('\n')
		case gap < -ref*1.5:
			w.b.WriteByte(' ')
		case gap > ref*0.2:
			bothWide := isWide(first) && isWide(w.lastRune)
			if !bothWide || gap > ref*0.5 {
				if w.lastRune != ' ' && first != ' ' {
					w.b.WriteByte(' ')
				}
			}
		}
	}
	w.b.WriteString(text)
	rs := []rune(text)
	w.lastRune = rs[len(rs)-1]
	w.lastX, w.lastY, w.lastSize, w.started = endX, y, size, true
}

// actualSpan is a marked-content sequence whose /ActualText replaces the
// glyphs drawn inside it (a ligature, a radical drawn for an ideograph).
type actualSpan struct {
	text    string
	written bool
}

type pdfReader struct {
	f         *pdfFile
	fontCache map[any]*pdfFont
	depth     int
}

func (r *pdfReader) run(content []byte, res pdfDict, gs gstate, w *pageWriter) {
	if r.depth > 12 {
		return
	}
	r.depth++
	defer func() { r.depth-- }()
	fonts := r.f.dict(res["Font"])
	xobjs := r.f.dict(res["XObject"])
	var stack []gstate
	var tm, tlm matrix
	l := &pdfLexer{b: content}
	var ops []any
	for {
		v, err := l.next()
		if err != nil {
			break
		}
		kw, isOp := v.(pdfKeyword)
		if !isOp {
			ops = append(ops, v)
			if len(ops) > 4096 {
				ops = ops[:0]
			}
			continue
		}
		op := string(kw)
		nums := func(n int) ([]float64, bool) {
			if len(ops) < n {
				return nil, false
			}
			out := make([]float64, n)
			for i := 0; i < n; i++ {
				f, ok := ops[len(ops)-n+i].(float64)
				if !ok {
					return nil, false
				}
				out[i] = f
			}
			return out, true
		}
		switch op {
		case "q":
			stack = append(stack, gs)
		case "Q":
			if len(stack) > 0 {
				gs = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		case "cm":
			if m, ok := nums(6); ok {
				gs.ctm = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}.mul(gs.ctm)
			}
		case "BT":
			tm, tlm = identity, identity
		case "Tf":
			if len(ops) >= 2 {
				if n, ok := ops[len(ops)-2].(pdfName); ok && fonts != nil {
					gs.font = r.f.loadFont(fonts[string(n)], r.fontCache)
				}
				if s, ok := ops[len(ops)-1].(float64); ok {
					gs.size = s
				}
			}
		case "Tc":
			if m, ok := nums(1); ok {
				gs.tc = m[0]
			}
		case "Tw":
			if m, ok := nums(1); ok {
				gs.tw = m[0]
			}
		case "Tz":
			if m, ok := nums(1); ok {
				gs.th = m[0] / 100
			}
		case "TL":
			if m, ok := nums(1); ok {
				gs.tl = m[0]
			}
		case "Ts":
			if m, ok := nums(1); ok {
				gs.rise = m[0]
			}
		case "Td", "TD":
			if m, ok := nums(2); ok {
				if op == "TD" {
					gs.tl = -m[1]
				}
				tlm = matrix{1, 0, 0, 1, m[0], m[1]}.mul(tlm)
				tm = tlm
			}
		case "Tm":
			if m, ok := nums(6); ok {
				tlm = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}
				tm = tlm
			}
		case "T*":
			tlm = matrix{1, 0, 0, 1, 0, -gs.tl}.mul(tlm)
			tm = tlm
		case "Tj", "'", "\"":
			if op != "Tj" {
				if op == "\"" {
					if m, ok := nums(3); ok {
						gs.tw, gs.tc = m[0], m[1]
					}
				}
				tlm = matrix{1, 0, 0, 1, 0, -gs.tl}.mul(tlm)
				tm = tlm
			}
			if len(ops) > 0 {
				if s, ok := ops[len(ops)-1].(pdfString); ok {
					r.show(s, &gs, &tm, w)
				}
			}
		case "TJ":
			if len(ops) > 0 {
				if arr, ok := ops[len(ops)-1].([]any); ok {
					for _, x := range arr {
						switch v := x.(type) {
						case pdfString:
							r.show(v, &gs, &tm, w)
						case float64:
							tx := -v / 1000 * gs.size * gs.th
							tm = matrix{1, 0, 0, 1, tx, 0}.mul(tm)
						}
					}
				}
			}
		case "Do":
			if len(ops) > 0 && xobjs != nil {
				if n, ok := ops[len(ops)-1].(pdfName); ok {
					if s := r.f.stream(xobjs[string(n)]); s != nil && name(s.dict["Subtype"]) == "Form" {
						sub := gs
						if m := r.f.array(s.dict["Matrix"]); len(m) == 6 {
							var mm matrix
							for i := range mm {
								mm[i], _ = numf(r.f.resolve(m[i]))
							}
							sub.ctm = mm.mul(gs.ctm)
						}
						fres := r.f.dict(s.dict["Resources"])
						if fres == nil {
							fres = res
						}
						if body, err := r.f.decode(s); err == nil {
							r.run(body, fres, sub, w)
						}
					}
				}
			}
		case "BMC", "BDC":
			var span *actualSpan
			if op == "BDC" && len(ops) > 0 {
				props, _ := ops[len(ops)-1].(pdfDict)
				if props == nil {
					if n, ok := ops[len(ops)-1].(pdfName); ok {
						props = r.f.dict(r.f.dict(res["Properties"])[string(n)])
					}
				}
				if at, ok := props["ActualText"].(pdfString); ok {
					span = &actualSpan{text: textString(at)}
				}
			}
			w.actual = append(w.actual, span)
		case "EMC":
			if len(w.actual) > 0 {
				w.actual = w.actual[:len(w.actual)-1]
			}
		case "BI":
			// An inline image: skip its data up to "EI".
			id := bytes.Index(content[l.pos:], []byte("ID"))
			if id < 0 {
				return
			}
			p := l.pos + id + 3
			for p < len(content) {
				i := bytes.Index(content[p:], []byte("EI"))
				if i < 0 {
					return
				}
				p += i
				if (p == 0 || isPDFSpace(content[p-1])) && (p+2 >= len(content) || isPDFSpace(content[p+2])) {
					break
				}
				p += 2
			}
			l.pos = min(len(content), p+2)
		}
		ops = ops[:0]
	}
}

func (r *pdfReader) show(s pdfString, gs *gstate, tm *matrix, w *pageWriter) {
	ft := gs.font
	if ft == nil {
		ft = &pdfFont{dw: 500}
		ft.simpleEncoding(r.f, nil)
	}
	th := gs.th
	if th == 0 {
		th = 1
	}
	for _, g := range ft.decode(s) {
		trm := matrix{gs.size * th, 0, 0, gs.size, 0, gs.rise}.mul(*tm).mul(gs.ctm)
		size := math.Hypot(trm[2], trm[3])
		adv := g.width/1000*gs.size + gs.tc
		if g.space {
			adv += gs.tw
		}
		adv *= th
		next := matrix{1, 0, 0, 1, adv, 0}.mul(*tm)
		end := matrix{gs.size * th, 0, 0, gs.size, 0, gs.rise}.mul(next).mul(gs.ctm)
		w.glyphs++
		text := g.text
		if g.known {
			w.known++
		}
		if strings.TrimSpace(text) == "" && text != "" {
			text = " "
		}
		if span := w.span(); span != nil {
			// The actual text stands for all the glyphs in the span.
			text = ""
			if !span.written {
				text, span.written = span.text, true
			}
			if !g.known {
				w.known++
			}
			if text == "" {
				w.lastX, w.lastY = end[4], trm[5]
				*tm = next
				continue
			}
		}
		w.place(text, trm[4], trm[5], end[4], size)
		*tm = next
	}
}

// pdfPages returns the pages in order, each with its inherited resources.
func (f *pdfFile) pages() []pdfDict {
	var out []pdfDict
	seen := map[pdfRef]bool{}
	var walk func(v any, inherited pdfDict, depth int)
	walk = func(v any, inherited pdfDict, depth int) {
		if depth > 64 {
			return
		}
		if r, ok := v.(pdfRef); ok {
			if seen[r] {
				return
			}
			seen[r] = true
		}
		d := f.dict(v)
		if d == nil {
			return
		}
		res := inherited
		if rd := f.dict(d["Resources"]); rd != nil {
			res = rd
		}
		if kids := f.array(d["Kids"]); kids != nil && name(d["Type"]) != "Page" {
			for _, k := range kids {
				walk(k, res, depth+1)
			}
			return
		}
		page := pdfDict{}
		for k, x := range d {
			page[k] = x
		}
		if res != nil {
			page["Resources"] = res
		}
		out = append(out, page)
	}
	if root := f.dict(f.trailer["Root"]); root != nil {
		walk(root["Pages"], nil, 0)
	}
	if len(out) == 0 { // no usable catalog: every page object, in number order
		nums := make([]int, 0)
		for n, v := range f.objs {
			if d, ok := v.(pdfDict); ok && name(d["Type"]) == "Page" {
				nums = append(nums, n)
			}
		}
		sortInts(nums)
		for _, n := range nums {
			out = append(out, f.objs[n].(pdfDict))
		}
	}
	return out
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// pdfResult is the text with how much of it the fonts could name.
type pdfResult struct {
	text          string
	pages         int
	glyphs, known int
}

func pdfInternal(data []byte) (pdfResult, error) {
	f, err := parsePDF(data)
	if err != nil {
		return pdfResult{}, err
	}
	rd := &pdfReader{f: f, fontCache: map[any]*pdfFont{}}
	var res pdfResult
	var b strings.Builder
	for i, p := range f.pages() {
		var content []byte
		switch c := f.resolve(p["Contents"]).(type) {
		case *pdfStream:
			content, _ = f.decode(c)
		case []any:
			for _, x := range c {
				if s := f.stream(x); s != nil {
					if d, err := f.decode(s); err == nil {
						content = append(content, d...)
						content = append(content, '\n')
					}
				}
			}
		}
		w := &pageWriter{}
		resources, _ := p["Resources"].(pdfDict)
		if resources == nil {
			resources = pdfDict{}
		}
		rd.run(content, resources, gstate{ctm: identity, th: 1}, w)
		res.pages++
		res.glyphs += w.glyphs
		res.known += w.known
		fmt.Fprintf(&b, "\n\n[第 %d 页]\n", i+1)
		b.WriteString(unwrap(w.b.String()))
	}
	res.text = b.String()
	return res, nil
}

// good says whether the text is worth keeping: most glyphs named, and more
// than a page number per page.
func (r pdfResult) good() bool {
	if r.glyphs == 0 {
		return false
	}
	if float64(r.known)/float64(r.glyphs) < 0.8 {
		return false
	}
	return readable(r.text) >= 20*r.pages || readable(r.text) >= 2000
}

// readable counts letters, digits and Chinese characters, ignoring the page
// markers.
func readable(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if r >= 0xE000 && r <= 0xF8FF {
				continue
			}
			n++
		}
	}
	return n
}

// pdf reads a PDF: x-mem's own reader first; when it cannot name most of the
// characters (fonts without a Unicode table, scans), the system's reader is
// tried and the better result kept.
func pdf(path string, data []byte) (string, error) {
	res, err := pdfInternal(data)
	if err == errPDFPassword {
		return "", err
	}
	if err == nil && res.good() {
		return res.text, nil
	}
	if alt, ok := externalPDF(path); ok && readable(alt) > readable(res.text) {
		return alt, nil
	}
	if err != nil {
		return "", err
	}
	if res.glyphs > 0 && float64(res.known)/float64(res.glyphs) < 0.5 {
		return "", fmt.Errorf("PDF 的字体没有带字符对照表，读出来是乱码（试试用 WPS/Acrobat 另存一份）")
	}
	if readable(res.text) == 0 {
		return "", ErrNoText
	}
	return res.text, nil
}

// unwrap joins the lines of a paragraph that the page layout broke: a line
// nearly as wide as the widest on the page, not ending a sentence, runs on
// into the next. Chinese words split at a line end are whole again, so they
// can be found.
func unwrap(page string) string {
	lines := strings.Split(page, "\n")
	width := func(s string) int {
		n := 0
		for _, r := range s {
			if isWide(r) {
				n += 2
			} else {
				n++
			}
		}
		return n
	}
	widest := 0
	for _, l := range lines {
		widest = max(widest, width(l))
	}
	if widest < 50 {
		return page
	}
	var b strings.Builder
	for i, l := range lines {
		b.WriteString(l)
		if i == len(lines)-1 {
			break
		}
		next := strings.TrimSpace(lines[i+1])
		t := strings.TrimRight(l, " ")
		if t == "" || next == "" || width(t)*100 < widest*85 {
			b.WriteByte('\n')
			continue
		}
		last, _ := utf8.DecodeLastRuneInString(t)
		first, _ := utf8.DecodeRuneInString(next)
		switch {
		case strings.ContainsRune("。！？；：.!?;:", last):
			b.WriteByte('\n')
		case isWide(last) && isWide(first):
			// run on without a space
		case last == '-' && unicode.IsLetter(first):
			// a hyphenated word: keep the hyphen, drop the break
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}
