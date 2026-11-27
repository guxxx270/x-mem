package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// The Office 97-2003 formats (.doc, .xls, .ppt) are streams inside a
// Compound File (CFB). This reads the container and then the text of each.

type cfb struct {
	data        []byte
	secSize     int
	fat         []uint32
	miniFat     []uint32
	miniStream  []byte
	cutoff      uint64
	entries     []cfbEntry
	miniSecSize int
}

type cfbEntry struct {
	name  string
	typ   byte
	start uint32
	size  uint64
}

const (
	endOfChain = 0xFFFFFFFE
	freeSect   = 0xFFFFFFFF
)

var cfbMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

func openCFB(data []byte) (*cfb, error) {
	if len(data) < 512 || !bytes.Equal(data[:8], cfbMagic) {
		return nil, errors.New("不是 Office 97-2003 文件（可能是改了扩展名的新格式或别的文件）")
	}
	le := binary.LittleEndian
	c := &cfb{data: data}
	shift := le.Uint16(data[0x1E:])
	if shift < 7 || shift > 16 {
		return nil, errors.New("文件头损坏")
	}
	c.secSize = 1 << shift
	c.miniSecSize = 1 << le.Uint16(data[0x20:])
	c.cutoff = uint64(le.Uint32(data[0x38:]))
	// The FAT sectors: 109 in the header, then the DIFAT chain.
	var fatSecs []uint32
	for i := 0; i < 109; i++ {
		s := le.Uint32(data[0x4C+4*i:])
		if s != freeSect && s != endOfChain {
			fatSecs = append(fatSecs, s)
		}
	}
	difat := le.Uint32(data[0x44:])
	for n := 0; difat != endOfChain && difat != freeSect && n < 1<<16; n++ {
		sec := c.sector(difat)
		if sec == nil {
			break
		}
		per := c.secSize/4 - 1
		for i := 0; i < per; i++ {
			s := le.Uint32(sec[4*i:])
			if s != freeSect && s != endOfChain {
				fatSecs = append(fatSecs, s)
			}
		}
		difat = le.Uint32(sec[4*per:])
	}
	for _, s := range fatSecs {
		sec := c.sector(s)
		if sec == nil {
			continue
		}
		for i := 0; i+4 <= len(sec); i += 4 {
			c.fat = append(c.fat, le.Uint32(sec[i:]))
		}
	}
	dir := c.chain(le.Uint32(data[0x30:]), 0)
	for i := 0; i+128 <= len(dir); i += 128 {
		e := dir[i : i+128]
		n := int(le.Uint16(e[0x40:]))
		if n > 64 {
			n = 64
		}
		name := decodeUTF16(e[:n], false)
		name = strings.TrimRight(name, "\x00")
		c.entries = append(c.entries, cfbEntry{name: name, typ: e[0x42], start: le.Uint32(e[0x74:]), size: le.Uint64(e[0x78:]) & 0xFFFFFFFF})
	}
	if len(c.entries) == 0 {
		return nil, errors.New("文件目录读不出来")
	}
	root := c.entries[0]
	c.miniStream = c.chain(root.start, root.size)
	mf := c.chain(le.Uint32(data[0x3C:]), 0)
	for i := 0; i+4 <= len(mf); i += 4 {
		c.miniFat = append(c.miniFat, le.Uint32(mf[i:]))
	}
	return c, nil
}

func (c *cfb) sector(n uint32) []byte {
	off := (int64(n) + 1) * int64(c.secSize)
	if off < 0 || off+int64(c.secSize) > int64(len(c.data)) {
		return nil
	}
	return c.data[off : off+int64(c.secSize)]
}

// chain reads a run of sectors from the FAT; size 0 means read it all.
func (c *cfb) chain(start uint32, size uint64) []byte {
	var out []byte
	seen := map[uint32]bool{}
	for s := start; s != endOfChain && s != freeSect && int(s) < len(c.fat)+1 && !seen[s]; {
		seen[s] = true
		sec := c.sector(s)
		if sec == nil {
			break
		}
		out = append(out, sec...)
		if size > 0 && uint64(len(out)) >= size {
			break
		}
		if int(s) >= len(c.fat) {
			break
		}
		s = c.fat[s]
	}
	if size > 0 && uint64(len(out)) > size {
		out = out[:size]
	}
	return out
}

func (c *cfb) miniChain(start uint32, size uint64) []byte {
	var out []byte
	seen := map[uint32]bool{}
	for s := start; s != endOfChain && s != freeSect && int(s) < len(c.miniFat) && !seen[s]; s = c.miniFat[s] {
		seen[s] = true
		off := int(s) * c.miniSecSize
		if off+c.miniSecSize > len(c.miniStream) {
			break
		}
		out = append(out, c.miniStream[off:off+c.miniSecSize]...)
		if uint64(len(out)) >= size {
			break
		}
	}
	if uint64(len(out)) > size {
		out = out[:size]
	}
	return out
}

// stream reads a stream by name (case-insensitive); nil if there is none.
func (c *cfb) stream(name string) []byte {
	for i, e := range c.entries {
		if i == 0 || e.typ != 2 || !strings.EqualFold(e.name, name) {
			continue
		}
		if e.size < c.cutoff {
			return c.miniChain(e.start, e.size)
		}
		return c.chain(e.start, e.size)
	}
	return nil
}

// ---- Word 97-2003 ----

func doc(_ string, data []byte) (string, error) {
	c, err := openCFB(data)
	if err != nil {
		return "", err
	}
	wd := c.stream("WordDocument")
	if len(wd) < 0x200 {
		return "", errors.New("没有 Word 正文流")
	}
	le := binary.LittleEndian
	if le.Uint16(wd) != 0xA5EC {
		return "", errors.New("Word 文件头不对（可能是 Word 6/95 的老格式）")
	}
	flags := le.Uint16(wd[0x0A:])
	if flags&0x0100 != 0 {
		return "", errors.New("文档加了密码，读不了")
	}
	table := "0Table"
	if flags&0x0200 != 0 {
		table = "1Table"
	}
	ts := c.stream(table)
	// FIB: 32-byte base, then rgW, rgLw and rgFcLcb, each with a count.
	pos := 32
	csw := int(le.Uint16(wd[pos:]))
	pos += 2 + 2*csw
	cslw := int(le.Uint16(wd[pos:]))
	pos += 2 + 4*cslw
	pos += 2 // cbRgFcLcb
	clxAt := pos + 33*8
	if clxAt+8 > len(wd) || ts == nil {
		return docFallback(wd), nil
	}
	fcClx, lcbClx := le.Uint32(wd[clxAt:]), le.Uint32(wd[clxAt+4:])
	if lcbClx == 0 || int(fcClx)+int(lcbClx) > len(ts) {
		return docFallback(wd), nil
	}
	clx := ts[fcClx : fcClx+lcbClx]
	i := 0
	for i < len(clx) && clx[i] == 0x01 { // Prc entries
		if i+3 > len(clx) {
			break
		}
		i += 3 + int(le.Uint16(clx[i+1:]))
	}
	if i+5 > len(clx) || clx[i] != 0x02 {
		return docFallback(wd), nil
	}
	lcb := int(le.Uint32(clx[i+1:]))
	plc := clx[i+5:]
	if lcb > len(plc) {
		lcb = len(plc)
	}
	n := (lcb - 4) / 12
	if n <= 0 {
		return docFallback(wd), nil
	}
	var b strings.Builder
	for k := 0; k < n; k++ {
		cpStart, cpEnd := le.Uint32(plc[4*k:]), le.Uint32(plc[4*(k+1):])
		pcd := plc[4*(n+1)+8*k:]
		fc := le.Uint32(pcd[2:])
		count := int(cpEnd) - int(cpStart)
		if count <= 0 || count > MaxText {
			continue
		}
		if fc&0x40000000 != 0 {
			off := int(fc&0x3FFFFFFF) / 2
			if off+count <= len(wd) {
				b.WriteString(decodeCP1252(wd[off : off+count]))
			}
		} else {
			off := int(fc)
			if off+2*count <= len(wd) {
				b.WriteString(decodeUTF16(wd[off:off+2*count], false))
			}
		}
	}
	return cleanWordChars(b.String()), nil
}

// docFallback reads the text range of a file without a piece table.
func docFallback(wd []byte) string {
	le := binary.LittleEndian
	start, end := le.Uint32(wd[0x18:]), le.Uint32(wd[0x1C:])
	if start >= end || int(end) > len(wd) {
		return ""
	}
	return cleanWordChars(decodeCP1252(wd[start:end]))
}

// cleanWordChars turns Word's special characters into text: paragraph and
// cell marks, and field codes (only the field result stays).
func cleanWordChars(s string) string {
	var b strings.Builder
	depth := 0
	var inCode []bool
	for _, r := range s {
		switch r {
		case 0x13: // field begin: the code follows
			depth++
			inCode = append(inCode, true)
			continue
		case 0x14: // separator: the result follows
			if depth > 0 {
				inCode[depth-1] = false
			}
			continue
		case 0x15: // field end
			if depth > 0 {
				depth--
				inCode = inCode[:depth]
			}
			continue
		}
		if depth > 0 && inCode[depth-1] {
			continue
		}
		switch r {
		case 0x0D, 0x0B, 0x0C:
			b.WriteByte('\n')
		case 0x07:
			b.WriteByte('\t')
		case 0x1E:
			b.WriteByte('-')
		case 0x1F, 0x01, 0x08, 0x02, 0x05:
		case 0xA0:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), "\t\t", "\t\n")
}

// ---- Excel 97-2003 ----

func xls(_ string, data []byte) (string, error) {
	c, err := openCFB(data)
	if err != nil {
		return "", err
	}
	wb := c.stream("Workbook")
	if wb == nil {
		if c.stream("Book") != nil {
			return "", errors.New("Excel 95 以前的格式不支持，另存为 .xlsx 就能读")
		}
		return "", errors.New("没有工作簿流")
	}
	le := binary.LittleEndian
	type rec struct {
		typ  uint16
		data []byte
	}
	var recs []rec
	for p := 0; p+4 <= len(wb); {
		t, n := le.Uint16(wb[p:]), int(le.Uint16(wb[p+2:]))
		if p+4+n > len(wb) {
			break
		}
		recs = append(recs, rec{t, wb[p+4 : p+4+n]})
		p += 4 + n
	}
	var sheets []string
	var sst []string
	for i, r := range recs {
		switch r.typ {
		case 0x002F:
			return "", errors.New("工作簿加了密码，读不了")
		case 0x0085: // BOUNDSHEET
			if len(r.data) > 8 {
				sheets = append(sheets, xlShortString(r.data[6:]))
			}
		case 0x00FC: // SST and its CONTINUE records
			parts := [][]byte{r.data}
			for j := i + 1; j < len(recs) && recs[j].typ == 0x003C; j++ {
				parts = append(parts, recs[j].data)
			}
			sst = parseSST(parts)
		}
	}
	type cell struct {
		row, col int
		val      string
	}
	var b strings.Builder
	sheet := -1
	var cells []cell
	flushSheet := func() {
		if sheet < 0 {
			return
		}
		name := fmt.Sprintf("Sheet%d", sheet+1)
		if sheet < len(sheets) {
			name = sheets[sheet]
		}
		fmt.Fprintf(&b, "\n\n[工作表 %s]\n", name)
		sort.Slice(cells, func(i, j int) bool {
			if cells[i].row != cells[j].row {
				return cells[i].row < cells[j].row
			}
			return cells[i].col < cells[j].col
		})
		for i := 0; i < len(cells); {
			j := i
			var line []string
			for j < len(cells) && cells[j].row == cells[i].row {
				line = append(line, cells[j].val)
				j++
			}
			b.WriteString(strings.Join(line, "\t") + "\n")
			i = j
		}
		cells = cells[:0]
	}
	inGlobals := true
	var lastFormula *cell
	for _, r := range recs {
		d := r.data
		switch r.typ {
		case 0x0809: // BOF
			if inGlobals {
				inGlobals = false
				continue
			}
			sheet++
		case 0x000A: // EOF
			if sheet >= 0 {
				flushSheet()
			}
		case 0x00FD: // LABELSST
			if len(d) >= 10 {
				if k := int(le.Uint32(d[6:])); k < len(sst) {
					cells = append(cells, cell{int(le.Uint16(d)), int(le.Uint16(d[2:])), sst[k]})
				}
			}
		case 0x0204: // LABEL
			if len(d) >= 9 {
				cells = append(cells, cell{int(le.Uint16(d)), int(le.Uint16(d[2:])), xlString(d[6:])})
			}
		case 0x0203: // NUMBER
			if len(d) >= 14 {
				cells = append(cells, cell{int(le.Uint16(d)), int(le.Uint16(d[2:])), fmtNum(math.Float64frombits(le.Uint64(d[6:])))})
			}
		case 0x027E: // RK
			if len(d) >= 10 {
				cells = append(cells, cell{int(le.Uint16(d)), int(le.Uint16(d[2:])), fmtNum(rk(le.Uint32(d[6:])))})
			}
		case 0x00BD: // MULRK
			if len(d) >= 6 {
				row, col := int(le.Uint16(d)), int(le.Uint16(d[2:]))
				for p := 4; p+6 <= len(d)-2; p += 6 {
					cells = append(cells, cell{row, col, fmtNum(rk(le.Uint32(d[p+2:])))})
					col++
				}
			}
		case 0x0006: // FORMULA: a numeric result is in the record, a string one follows
			lastFormula = nil
			if len(d) >= 14 {
				row, col := int(le.Uint16(d)), int(le.Uint16(d[2:]))
				if le.Uint16(d[12:]) != 0xFFFF {
					cells = append(cells, cell{row, col, fmtNum(math.Float64frombits(le.Uint64(d[6:])))})
				} else if d[6] == 0 {
					lastFormula = &cell{row: row, col: col}
				}
			}
		case 0x0207: // STRING
			if lastFormula != nil && len(d) >= 3 {
				lastFormula.val = xlString(d)
				cells = append(cells, *lastFormula)
				lastFormula = nil
			}
		}
	}
	return b.String(), nil
}

func rk(v uint32) float64 {
	var f float64
	if v&2 != 0 {
		f = float64(int32(v) >> 2)
	} else {
		f = math.Float64frombits(uint64(v&0xFFFFFFFC) << 32)
	}
	if v&1 != 0 {
		f /= 100
	}
	return f
}

func fmtNum(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 12, 64)
}

// xlShortString: 1-byte length, flags, characters.
func xlShortString(d []byte) string {
	if len(d) < 2 {
		return ""
	}
	n, high := int(d[0]), d[1]&1 != 0
	return xlChars(d[2:], n, high)
}

// xlString: 2-byte length, flags, characters.
func xlString(d []byte) string {
	if len(d) < 3 {
		return ""
	}
	n, high := int(binary.LittleEndian.Uint16(d)), d[2]&1 != 0
	return xlChars(d[3:], n, high)
}

func xlChars(d []byte, n int, high bool) string {
	if high {
		if 2*n > len(d) {
			n = len(d) / 2
		}
		return decodeUTF16(d[:2*n], false)
	}
	if n > len(d) {
		n = len(d)
	}
	return decodeCP1252(d[:n])
}

// parseSST reads the shared strings; a string's characters may continue in
// the next CONTINUE record, which then starts with a new flags byte.
func parseSST(parts [][]byte) []string {
	le := binary.LittleEndian
	if len(parts) == 0 || len(parts[0]) < 8 {
		return nil
	}
	total := int(le.Uint32(parts[0][4:]))
	part, pos := 0, 8
	cur := parts[0]
	next := func() bool {
		part++
		if part >= len(parts) {
			return false
		}
		cur, pos = parts[part], 0
		return true
	}
	readN := func(n int) []byte {
		var out []byte
		for n > 0 {
			if pos >= len(cur) && !next() {
				return out
			}
			k := len(cur) - pos
			if k > n {
				k = n
			}
			out = append(out, cur[pos:pos+k]...)
			pos += k
			n -= k
		}
		return out
	}
	var out []string
	for len(out) < total && total < 1<<24 {
		hdr := readN(3)
		if len(hdr) < 3 {
			break
		}
		cch, flags := int(le.Uint16(hdr)), hdr[2]
		runs, ext := 0, 0
		if flags&0x08 != 0 {
			runs = int(le.Uint16(readN(2)))
		}
		if flags&0x04 != 0 {
			ext = int(le.Uint32(readN(4)))
		}
		high := flags&1 != 0
		var sb strings.Builder
		left := cch
		for left > 0 {
			if pos >= len(cur) {
				if !next() {
					break
				}
				high = cur[0]&1 != 0
				pos = 1
			}
			width := 1
			if high {
				width = 2
			}
			k := (len(cur) - pos) / width
			if k > left {
				k = left
			}
			if k == 0 {
				pos = len(cur)
				continue
			}
			if high {
				sb.WriteString(decodeUTF16(cur[pos:pos+2*k], false))
			} else {
				sb.WriteString(decodeCP1252(cur[pos : pos+k]))
			}
			pos += k * width
			left -= k
		}
		readN(4*runs + ext)
		out = append(out, sb.String())
	}
	return out
}

// ---- PowerPoint 97-2003 ----

// pptLine writes one text atom, leaving out placeholder marks ("*" stands
// for the slide number).
func pptLine(b *strings.Builder, s string) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", "\n"))
	if s == "" || s == "*" {
		return
	}
	b.WriteString(s + "\n")
}

func ppt(_ string, data []byte) (string, error) {
	c, err := openCFB(data)
	if err != nil {
		return "", err
	}
	s := c.stream("PowerPoint Document")
	if s == nil {
		return "", errors.New("没有演示文稿流")
	}
	var b strings.Builder
	// Slide numbers are left out: depending on the program that wrote the
	// file, the text sits in the slide list or in the slides themselves.
	// list is the instance of the SlideListWithText being walked: 0 slides,
	// 1 masters, 2 notes; -1 outside one.
	var walk func(d []byte, depth, list int)
	walk = func(d []byte, depth, list int) {
		le := binary.LittleEndian
		for p := 0; p+8 <= len(d); {
			verInst, typ, n := le.Uint16(d[p:]), le.Uint16(d[p+2:]), int(le.Uint32(d[p+4:]))
			body := p + 8
			if n < 0 || body+n > len(d) {
				return
			}
			inst := int(verInst >> 4)
			switch {
			case typ == 0x03F8 || typ == 0x0FC3: // MainMaster / Handout: template text
			case typ == 0x0FF0 && depth < 32: // SlideListWithText
				if inst != 1 {
					walk(d[body:body+n], depth+1, inst)
				}
			case verInst&0x0F == 0x0F && depth < 32: // a container
				walk(d[body:body+n], depth+1, list)
			case typ == 0x0FA0: // TextCharsAtom
				pptLine(&b, decodeUTF16(d[body:body+n], false))
			case typ == 0x0FA8: // TextBytesAtom
				pptLine(&b, decodeCP1252(d[body:body+n]))
			}
			p = body + n
		}
	}
	walk(s, 0, -1)
	return b.String(), nil
}
