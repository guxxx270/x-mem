package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The zip-and-XML formats: Office Open XML (docx, pptx, xlsx), OpenDocument
// (odt, ods, odp) and EPUB.

type zipDoc struct {
	files map[string]*zip.File
}

func openZip(data []byte) (*zipDoc, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的压缩包格式：%v", err)
	}
	z := &zipDoc{files: map[string]*zip.File{}}
	for _, f := range zr.File {
		z.files[strings.TrimPrefix(f.Name, "/")] = f
	}
	return z, nil
}

func (z *zipDoc) read(name string) []byte {
	f, ok := z.files[strings.TrimPrefix(name, "/")]
	if !ok {
		return nil
	}
	if f.UncompressedSize64 > MaxFileSize {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, _ := io.ReadAll(io.LimitReader(rc, MaxFileSize))
	return data
}

// numbered lists parts like ppt/slides/slide12.xml in numeric order.
func (z *zipDoc) numbered(re *regexp.Regexp) []string {
	type part struct {
		name string
		n    int
	}
	var parts []part
	for name := range z.files {
		if m := re.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			parts = append(parts, part{name, n})
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].n < parts[j].n })
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.name
	}
	return out
}

// rels reads a .rels part: relationship id → target, resolved against dir.
func (z *zipDoc) rels(relsPath, dir string) map[string]string {
	out := map[string]string{}
	data := z.read(relsPath)
	if data == nil {
		return out
	}
	var r struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(data, &r) != nil {
		return out
	}
	for _, x := range r.Rel {
		if x.Mode == "External" {
			continue
		}
		t := x.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Clean(path.Join(dir, t))
		}
		out[x.ID] = t
	}
	return out
}

func newDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(decodeCharset(b, charset)), nil
	}
	return d
}

// ---- Word ----

func docx(_ string, data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	main := "word/document.xml"
	for id, t := range z.rels("_rels/.rels", "") {
		_ = id
		if strings.HasSuffix(t, "document.xml") || strings.HasSuffix(t, "document2.xml") {
			main = t
		}
	}
	body := z.read(main)
	if body == nil {
		return "", fmt.Errorf("找不到正文（%s）", main)
	}
	var b strings.Builder
	b.WriteString(wordXML(body))
	for _, part := range []string{"word/footnotes.xml", "word/endnotes.xml", "word/comments.xml"} {
		if t := strings.TrimSpace(wordXML(z.read(part))); t != "" {
			b.WriteString("\n\n" + t)
		}
	}
	return b.String(), nil
}

// wordXML reads WordprocessingML: paragraphs, tabs, breaks, table cells.
func wordXML(data []byte) string {
	if data == nil {
		return ""
	}
	d := newDecoder(data)
	var b strings.Builder
	inText, inDel := false, 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = inDel == 0
			case "del", "instrText", "delText":
				inDel++
			case "tab", "ptab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			case "noBreakHyphen":
				b.WriteByte('-')
			case "sym":
				for _, a := range t.Attr {
					if a.Name.Local == "char" {
						if v, err := strconv.ParseUint(a.Value, 16, 32); err == nil && v >= 0x20 && v < 0xF000 {
							b.WriteRune(rune(v))
						}
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "del", "instrText", "delText":
				if inDel > 0 {
					inDel--
				}
			case "p":
				b.WriteByte('\n')
			case "tc":
				b.WriteByte('\t')
			case "tr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return cleanCells(b.String())
}

// cleanCells turns "a\n\tb\n\t\n" (paragraphs inside table cells) into
// tidier lines.
func cleanCells(s string) string {
	s = strings.ReplaceAll(s, "\n\t", "\t")
	s = strings.ReplaceAll(s, "\t\n", "\n")
	return s
}

// ---- PowerPoint ----

var slideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func pptx(_ string, data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	slides := z.numbered(slideRe)
	if len(slides) == 0 {
		return "", fmt.Errorf("没有幻灯片")
	}
	var b strings.Builder
	for i, s := range slides {
		fmt.Fprintf(&b, "\n\n[幻灯片 %d]\n", i+1)
		b.WriteString(drawingXML(z.read(s)))
		dir, file := path.Split(s)
		for _, t := range z.rels(dir+"_rels/"+file+".rels", dir) {
			if strings.Contains(t, "notesSlide") {
				if notes := strings.TrimSpace(drawingXML(z.read(t))); notes != "" && !isSlideNumberOnly(notes) {
					b.WriteString("\n备注：" + notes)
				}
			}
		}
	}
	return b.String(), nil
}

func isSlideNumberOnly(s string) bool {
	_, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil
}

// drawingXML reads DrawingML text: a:p paragraphs of a:t runs.
func drawingXML(data []byte) string {
	if data == nil {
		return ""
	}
	d := newDecoder(data)
	var b strings.Builder
	inText := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			case "tc":
				b.WriteByte('\t')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return cleanCells(b.String())
}

// ---- Excel ----

const maxRows = 50000

func xlsx(_ string, data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	shared := sharedStrings(z.read("xl/sharedStrings.xml"))
	var wb struct {
		Sheets []struct {
			Name  string `xml:"name,attr"`
			RID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			State string `xml:"state,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(z.read("xl/workbook.xml"), &wb); err != nil {
		return "", fmt.Errorf("工作簿读不懂：%v", err)
	}
	rels := z.rels("xl/_rels/workbook.xml.rels", "xl")
	var b strings.Builder
	rows := 0
	for _, s := range wb.Sheets {
		target := rels[s.RID]
		if target == "" || s.State == "veryHidden" {
			continue
		}
		fmt.Fprintf(&b, "\n\n[工作表 %s]\n", s.Name)
		rows += sheetXML(&b, z.read(target), shared, maxRows-rows)
		if rows >= maxRows {
			b.WriteString("\n……（行数太多，后面没有收进索引）")
			break
		}
	}
	return b.String(), nil
}

func sharedStrings(data []byte) []string {
	if data == nil {
		return nil
	}
	d := newDecoder(data)
	var out []string
	var cur strings.Builder
	inT, inPh := false, 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = inPh == 0
			case "rPh":
				inPh++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "t":
				inT = false
			case "rPh":
				inPh--
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out
}

// sheetXML writes one worksheet as tab-separated lines; it returns the rows
// written.
func sheetXML(b *strings.Builder, data []byte, shared []string, budget int) int {
	if data == nil || budget <= 0 {
		return 0
	}
	d := newDecoder(data)
	var row []string
	var cellType, val string
	inV, inIS := false, false
	n := 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = row[:0]
			case "c":
				cellType, val = "", ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				if inIS {
					inV = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				if inIS {
					inV = false
				}
			case "is":
				inIS = false
			case "c":
				if cellType == "s" {
					if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && i >= 0 && i < len(shared) {
						val = shared[i]
					}
				} else if cellType == "b" {
					val = map[string]string{"1": "TRUE", "0": "FALSE"}[val]
				}
				row = append(row, strings.TrimSpace(val))
			case "row":
				for len(row) > 0 && row[len(row)-1] == "" {
					row = row[:len(row)-1]
				}
				if len(row) > 0 {
					b.WriteString(strings.Join(row, "\t") + "\n")
					n++
					if n >= budget {
						return n
					}
				}
			}
		case xml.CharData:
			if inV {
				val += string(t)
			}
		}
	}
	return n
}

// ---- OpenDocument ----

func odf(_ string, data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	content := z.read("content.xml")
	if content == nil {
		return "", fmt.Errorf("没有 content.xml")
	}
	d := newDecoder(content)
	var b strings.Builder
	cell, page := 0, 0
	mime := string(z.read("mimetype"))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "s":
				n := 1
				for _, a := range t.Attr {
					if a.Name.Local == "c" {
						n, _ = strconv.Atoi(a.Value)
					}
				}
				if n > 0 && n < 100 {
					b.WriteString(strings.Repeat(" ", n))
				}
			case "tab":
				b.WriteByte('\t')
			case "line-break":
				b.WriteByte('\n')
			case "table-cell":
				cell++
			case "page":
				page++
				fmt.Fprintf(&b, "\n\n[幻灯片 %d]\n", page)
			case "table":
				if strings.Contains(mime, "spreadsheet") {
					for _, a := range t.Attr {
						if a.Name.Local == "name" {
							fmt.Fprintf(&b, "\n\n[工作表 %s]\n", a.Value)
						}
					}
				}
			case "note-citation", "tracked-changes", "annotation", "page-number":
				d.Skip()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "h":
				if cell > 0 {
					b.WriteByte(' ')
				} else {
					b.WriteByte('\n')
				}
			case "table-cell":
				cell--
				b.WriteByte('\t')
			case "table-row":
				b.WriteByte('\n')
			}
		case xml.CharData:
			b.Write(t)
		}
	}
	return cleanCells(b.String()), nil
}

// ---- EPUB ----

func epub(_ string, data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(z.read("META-INF/container.xml"), &container) != nil || len(container.Rootfiles) == 0 {
		return "", fmt.Errorf("EPUB 缺少 container.xml")
	}
	opfPath := container.Rootfiles[0].Path
	var opf struct {
		Items []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(z.read(opfPath), &opf); err != nil {
		return "", fmt.Errorf("EPUB 目录读不懂：%v", err)
	}
	href := map[string]string{}
	for _, it := range opf.Items {
		href[it.ID] = it.Href
	}
	dir := path.Dir(opfPath)
	var b strings.Builder
	for _, s := range opf.Spine {
		h := href[s.IDRef]
		if h == "" {
			continue
		}
		p := path.Clean(path.Join(dir, h))
		if html := z.read(p); html != nil {
			b.WriteString(htmlText(html) + "\n\n")
		}
	}
	return b.String(), nil
}

// ---- plain XML ----

func xmlText(_ string, data []byte) (string, error) {
	d := newDecoder(data)
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if c, ok := tok.(xml.CharData); ok {
			if s := strings.TrimSpace(string(c)); s != "" {
				b.WriteString(s + "\n")
			}
		}
	}
	return b.String(), nil
}
