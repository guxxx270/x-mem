package extract

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strconv"
)

// A small PDF object reader: enough to find pages, fonts and content
// streams in files written by Word, WPS, LibreOffice, browsers and scanners,
// including compressed object streams and files encrypted without an open
// password.

type pdfName string
type pdfString []byte
type pdfKeyword string
type pdfDict map[string]any
type pdfRef struct{ num, gen int }

type pdfStream struct {
	dict     pdfDict
	raw      []byte
	start    int // offset of the data in the file
	num, gen int
}

type pdfFile struct {
	data    []byte
	objs    map[int]any // object number → value (the last definition wins)
	gens    map[int]int
	trailer pdfDict
	crypt   *pdfCrypt
}

// ---- lexer ----

type pdfLexer struct {
	b   []byte
	pos int
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isPDFDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *pdfLexer) skipSpace() {
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		if isPDFSpace(c) {
			l.pos++
		} else if c == '%' {
			for l.pos < len(l.b) && l.b[l.pos] != '\n' && l.b[l.pos] != '\r' {
				l.pos++
			}
		} else {
			return
		}
	}
}

var errPDFEnd = errors.New("end of data")

// next reads one object or keyword. Arrays and dictionaries are read whole;
// "R" references are resolved from "n g R".
func (l *pdfLexer) next() (any, error) {
	l.skipSpace()
	if l.pos >= len(l.b) {
		return nil, errPDFEnd
	}
	c := l.b[l.pos]
	switch {
	case c == '/':
		l.pos++
		start := l.pos
		for l.pos < len(l.b) && !isPDFSpace(l.b[l.pos]) && !isPDFDelim(l.b[l.pos]) {
			l.pos++
		}
		return pdfName(unescapeName(l.b[start:l.pos])), nil
	case c == '(':
		return l.literal(), nil
	case c == '<':
		if l.pos+1 < len(l.b) && l.b[l.pos+1] == '<' {
			l.pos += 2
			return l.dict()
		}
		l.pos++
		start := l.pos
		for l.pos < len(l.b) && l.b[l.pos] != '>' {
			l.pos++
		}
		h := l.b[start:l.pos]
		l.pos++
		return pdfString(hexBytes(h)), nil
	case c == '[':
		l.pos++
		var arr []any
		for {
			l.skipSpace()
			if l.pos >= len(l.b) {
				return arr, nil
			}
			if l.b[l.pos] == ']' {
				l.pos++
				return arr, nil
			}
			v, err := l.next()
			if err != nil {
				return arr, nil
			}
			if k, ok := v.(pdfKeyword); ok && k == "R" && len(arr) >= 2 {
				if n, ok1 := arr[len(arr)-2].(float64); ok1 {
					if g, ok2 := arr[len(arr)-1].(float64); ok2 {
						arr = append(arr[:len(arr)-2], pdfRef{int(n), int(g)})
						continue
					}
				}
			}
			arr = append(arr, v)
		}
	case c == ']' || c == '>' || c == ')' || c == '}' || c == '{':
		l.pos++
		if c == '>' && l.pos < len(l.b) && l.b[l.pos] == '>' {
			l.pos++
			return pdfKeyword(">>"), nil
		}
		return pdfKeyword(string(c)), nil
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		start := l.pos
		l.pos++
		for l.pos < len(l.b) && (l.b[l.pos] >= '0' && l.b[l.pos] <= '9' || l.b[l.pos] == '.' || l.b[l.pos] == '-') {
			l.pos++
		}
		f, err := strconv.ParseFloat(string(l.b[start:l.pos]), 64)
		if err != nil {
			f = 0
		}
		return f, nil
	}
	start := l.pos
	for l.pos < len(l.b) && !isPDFSpace(l.b[l.pos]) && !isPDFDelim(l.b[l.pos]) {
		l.pos++
	}
	if l.pos == start {
		l.pos++
		return pdfKeyword(string(c)), nil
	}
	w := string(l.b[start:l.pos])
	switch w {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	return pdfKeyword(w), nil
}

func (l *pdfLexer) dict() (pdfDict, error) {
	d := pdfDict{}
	for {
		l.skipSpace()
		if l.pos >= len(l.b) {
			return d, nil
		}
		if l.b[l.pos] == '>' {
			l.pos++
			if l.pos < len(l.b) && l.b[l.pos] == '>' {
				l.pos++
			}
			return d, nil
		}
		k, err := l.next()
		if err != nil {
			return d, nil
		}
		key, ok := k.(pdfName)
		if !ok {
			continue
		}
		v, err := l.value()
		if err != nil {
			return d, nil
		}
		d[string(key)] = v
	}
}

var refAhead = regexp.MustCompile(`^\s*(\d+)\s+R\b`)

// value reads an object, turning "n g R" into a reference.
func (l *pdfLexer) value() (any, error) {
	v, err := l.next()
	if err != nil {
		return nil, err
	}
	if f, ok := v.(float64); ok && f == float64(int(f)) {
		if m := refAhead.FindSubmatch(l.b[l.pos:min(len(l.b), l.pos+24)]); m != nil {
			g, _ := strconv.Atoi(string(m[1]))
			l.pos += len(m[0])
			return pdfRef{int(f), g}, nil
		}
	}
	return v, nil
}

func (l *pdfLexer) literal() pdfString {
	l.pos++ // (
	var out []byte
	depth := 1
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		l.pos++
		switch c {
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return out
			}
			out = append(out, c)
		case '\\':
			if l.pos >= len(l.b) {
				return out
			}
			e := l.b[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				if l.pos < len(l.b) && l.b[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.b) && l.b[l.pos] >= '0' && l.b[l.pos] <= '7'; k++ {
						v = v*8 + int(l.b[l.pos]-'0')
						l.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		default:
			out = append(out, c)
		}
	}
	return out
}

func hexBytes(h []byte) []byte {
	clean := make([]byte, 0, len(h))
	for _, c := range h {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			clean = append(clean, c)
		}
	}
	if len(clean)%2 == 1 {
		clean = append(clean, '0')
	}
	out := make([]byte, len(clean)/2)
	hex.Decode(out, clean)
	return out
}

func unescapeName(b []byte) string {
	if bytes.IndexByte(b, '#') < 0 {
		return string(b)
	}
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] == '#' && i+2 < len(b) {
			if v, err := strconv.ParseUint(string(b[i+1:i+3]), 16, 8); err == nil {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, b[i])
	}
	return string(out)
}

// ---- file ----

var objHeader = regexp.MustCompile(`(\d+)[ \t\r\n\f\x00]+(\d+)[ \t\r\n\f\x00]+obj\b`)

func parsePDF(data []byte) (*pdfFile, error) {
	if !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF")) {
		return nil, errors.New("不是 PDF 文件")
	}
	f := &pdfFile{data: data, objs: map[int]any{}, gens: map[int]int{}}
	var streams []*pdfStream
	pos := 0
	for pos < len(data) {
		loc := objHeader.FindSubmatchIndex(data[pos:])
		if loc == nil {
			break
		}
		num, _ := strconv.Atoi(string(data[pos+loc[2] : pos+loc[3]]))
		gen, _ := strconv.Atoi(string(data[pos+loc[4] : pos+loc[5]]))
		l := &pdfLexer{b: data, pos: pos + loc[1]}
		v, err := l.value()
		if err != nil {
			break
		}
		next := l.pos
		if d, ok := v.(pdfDict); ok {
			l.skipSpace()
			if bytes.HasPrefix(data[l.pos:], []byte("stream")) {
				s, end := readStream(data, l.pos+6, d)
				s.num, s.gen = num, gen
				v = s
				streams = append(streams, s)
				next = end
			}
		}
		f.objs[num], f.gens[num] = v, gen
		pos = next
	}
	// A /Length given as a reference is known only now: binary data can
	// contain the word "endstream", so trust the length when it fits.
	for _, s := range streams {
		if r, ok := s.dict["Length"].(pdfRef); ok {
			if n, ok := f.objs[r.num].(float64); ok && n >= 0 && s.start+int(n) <= len(data) {
				end := s.start + int(n)
				rest := data[end:min(len(data), end+32)]
				if i := bytes.Index(rest, []byte("endstream")); i >= 0 && len(bytes.TrimSpace(rest[:i])) == 0 {
					s.raw = data[s.start:end]
				}
			}
		}
	}
	// Trailers: the last one wins; a cross-reference stream carries the
	// same keys.
	f.trailer = pdfDict{}
	for i := bytes.Index(data, []byte("trailer")); i >= 0; {
		l := &pdfLexer{b: data, pos: i + 7}
		if v, err := l.next(); err == nil {
			if d, ok := v.(pdfDict); ok {
				for k, x := range d {
					f.trailer[k] = x
				}
			}
		}
		j := bytes.Index(data[i+7:], []byte("trailer"))
		if j < 0 {
			break
		}
		i += 7 + j
	}
	for _, s := range streams {
		if name(s.dict["Type"]) == "XRef" {
			for _, k := range []string{"Root", "Encrypt", "ID", "Info"} {
				if x, ok := s.dict[k]; ok {
					f.trailer[k] = x
				}
			}
		}
	}
	if enc := f.dict(f.trailer["Encrypt"]); enc != nil {
		var id0 []byte
		if ids, ok := f.trailer["ID"].([]any); ok && len(ids) > 0 {
			id0, _ = ids[0].(pdfString)
		}
		c, err := newPDFCrypt(enc, id0)
		if err != nil {
			return nil, err
		}
		f.crypt = c
	}
	// Objects packed in object streams.
	for _, s := range streams {
		if name(s.dict["Type"]) != "ObjStm" {
			continue
		}
		body, err := f.decode(s)
		if err != nil {
			continue
		}
		n, first := num(s.dict["N"]), num(s.dict["First"])
		l := &pdfLexer{b: body}
		var pairs [][2]int
		for k := 0; k < n; k++ {
			a, err1 := l.next()
			b, err2 := l.next()
			if err1 != nil || err2 != nil {
				break
			}
			pairs = append(pairs, [2]int{int(num(a)), int(num(b))})
		}
		for _, p := range pairs {
			if _, have := f.objs[p[0]]; have {
				continue
			}
			ol := &pdfLexer{b: body, pos: first + p[1]}
			if ol.pos >= len(body) {
				continue
			}
			if v, err := ol.value(); err == nil {
				f.objs[p[0]] = v
			}
		}
	}
	return f, nil
}

// readStream finds a stream's bytes, trusting /Length when it is direct and
// lands on "endstream", and searching for "endstream" otherwise.
func readStream(data []byte, at int, d pdfDict) (*pdfStream, int) {
	if at < len(data) && data[at] == '\r' {
		at++
	}
	if at < len(data) && data[at] == '\n' {
		at++
	}
	if n, ok := d["Length"].(float64); ok && n >= 0 && at+int(n) <= len(data) {
		end := at + int(n)
		rest := data[end:min(len(data), end+32)]
		if i := bytes.Index(rest, []byte("endstream")); i >= 0 && len(bytes.TrimSpace(rest[:i])) == 0 {
			return &pdfStream{dict: d, raw: data[at:end], start: at}, end + i + 9
		}
	}
	i := bytes.Index(data[at:], []byte("endstream"))
	if i < 0 {
		return &pdfStream{dict: d, raw: data[at:], start: at}, len(data)
	}
	raw := data[at : at+i]
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	return &pdfStream{dict: d, raw: raw, start: at}, at + i + 9
}

func (f *pdfFile) resolve(v any) any {
	for depth := 0; depth < 32; depth++ {
		r, ok := v.(pdfRef)
		if !ok {
			return v
		}
		v = f.objs[r.num]
	}
	return nil
}

func (f *pdfFile) dict(v any) pdfDict {
	switch x := f.resolve(v).(type) {
	case pdfDict:
		return x
	case *pdfStream:
		return x.dict
	}
	return nil
}

func (f *pdfFile) stream(v any) *pdfStream {
	s, _ := f.resolve(v).(*pdfStream)
	return s
}

func (f *pdfFile) array(v any) []any {
	a, _ := f.resolve(v).([]any)
	return a
}

func name(v any) string {
	n, _ := v.(pdfName)
	return string(n)
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func numf(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// decode returns a stream's data with its filters undone.
func (f *pdfFile) decode(s *pdfStream) ([]byte, error) {
	data := s.raw
	if f.crypt != nil && name(s.dict["Type"]) != "XRef" {
		data = f.crypt.decrypt(s.num, s.gen, data, true)
	}
	var filters []any
	switch x := f.resolve(s.dict["Filter"]).(type) {
	case pdfName:
		filters = []any{x}
	case []any:
		filters = x
	}
	var parms []any
	switch x := f.resolve(s.dict["DecodeParms"]).(type) {
	case pdfDict:
		parms = []any{x}
	case []any:
		parms = x
	}
	for i, fl := range filters {
		var p pdfDict
		if i < len(parms) {
			p = f.dict(parms[i])
		}
		var err error
		switch name(f.resolve(fl)) {
		case "FlateDecode", "Fl":
			data, err = inflate(data)
			if err == nil && p != nil && num(p["Predictor"]) >= 10 {
				data = unPredict(data, max(1, num(p["Columns"])), max(1, num(p["Colors"])), max(8, num(p["BitsPerComponent"])))
			}
		case "ASCIIHexDecode", "AHx":
			if i := bytes.IndexByte(data, '>'); i >= 0 {
				data = data[:i]
			}
			data = hexBytes(data)
		case "ASCII85Decode", "A85":
			data, err = ascii85(data)
		case "RunLengthDecode", "RL":
			data = runLength(data)
		case "LZWDecode", "LZW":
			data, err = lzwDecode(data, p == nil || p["EarlyChange"] == nil || num(p["EarlyChange"]) != 0)
		case "Crypt":
		default:
			return nil, errors.New("image data")
		}
		if err != nil && len(data) == 0 {
			return nil, err
		}
	}
	return data, nil
}

// inflate undoes zlib compression, keeping what it could read from a
// damaged stream.
func inflate(data []byte) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		out, err := io.ReadAll(io.LimitReader(zr, MaxFileSize))
		if len(out) > 0 || err == nil {
			return out, nil
		}
	}
	if len(data) > 2 {
		out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(data[2:])), MaxFileSize))
		if len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
	return nil, errors.New("压缩数据损坏")
}

// unPredict reverses PNG row prediction.
func unPredict(data []byte, columns, colors, bits int) []byte {
	bpp := max(1, colors*bits/8)
	row := columns * colors * bits / 8
	if row <= 0 {
		return data
	}
	var out []byte
	prev := make([]byte, row)
	for p := 0; p+row+1 <= len(data); p += row + 1 {
		ft := data[p]
		cur := append([]byte(nil), data[p+1:p+1+row]...)
		for i := range cur {
			var a, b, c byte
			if i >= bpp {
				a = cur[i-bpp]
				c = prev[i-bpp]
			}
			b = prev[i]
			switch ft {
			case 1:
				cur[i] += a
			case 2:
				cur[i] += b
			case 3:
				cur[i] += byte((int(a) + int(b)) / 2)
			case 4:
				cur[i] += paeth(a, b, c)
			}
		}
		out = append(out, cur...)
		prev = cur
	}
	return out
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func ascii85(data []byte) ([]byte, error) {
	var out []byte
	var group []byte
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '~' {
			break
		}
		if isPDFSpace(c) {
			continue
		}
		if c == 'z' && len(group) == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c < '!' || c > 'u' {
			return out, errors.New("ASCII85 数据有误")
		}
		group = append(group, c)
		if len(group) == 5 {
			out = append(out, a85group(group, 4)...)
			group = group[:0]
		}
	}
	if n := len(group); n > 1 {
		for len(group) < 5 {
			group = append(group, 'u')
		}
		out = append(out, a85group(group, n-1)...)
	}
	return out, nil
}

func a85group(g []byte, keep int) []byte {
	var v uint32
	for _, c := range g {
		v = v*85 + uint32(c-'!')
	}
	b := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	return b[:keep]
}

func runLength(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out
		case n < 128:
			end := min(len(data), i+n+1)
			out = append(out, data[i:end]...)
			i = end
		default:
			if i < len(data) {
				out = append(out, bytes.Repeat(data[i:i+1], 257-n)...)
				i++
			}
		}
	}
	return out
}

// lzwDecode reads PDF's LZW (variable code width, early change by default).
func lzwDecode(data []byte, early bool) ([]byte, error) {
	var out []byte
	dict := make([][]byte, 258, 4096)
	reset := func() {
		dict = dict[:258]
		for i := 0; i < 256; i++ {
			dict[i] = []byte{byte(i)}
		}
	}
	reset()
	width := 9
	var buf uint32
	var nbits int
	var prev []byte
	e := 0
	if early {
		e = 1
	}
	for i := 0; i < len(data); i++ {
		buf = buf<<8 | uint32(data[i])
		nbits += 8
		for nbits >= width {
			code := int(buf >> (nbits - width) & (1<<width - 1))
			nbits -= width
			switch {
			case code == 256:
				reset()
				width, prev = 9, nil
				continue
			case code == 257:
				return out, nil
			}
			var entry []byte
			if code < len(dict) {
				entry = dict[code]
			} else if prev != nil {
				entry = append(append([]byte(nil), prev...), prev[0])
			} else {
				return out, errors.New("LZW 数据有误")
			}
			out = append(out, entry...)
			if prev != nil && len(dict) < 4096 {
				dict = append(dict, append(append([]byte(nil), prev...), entry[0]))
			}
			prev = entry
			if len(dict)+e >= 1<<width && width < 12 {
				width++
			}
		}
	}
	return out, nil
}
