// Package extract turns documents into plain text for the knowledge base:
// Word, PowerPoint and Excel (new and 97-2003), PDF, OpenDocument, EPUB, RTF,
// HTML and plain text, including Chinese text in GBK. Everything is read
// with the standard library; on macOS the system's own PDF reader is tried
// when a PDF yields little text.
package extract

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Limits keep one huge file from slowing everything down.
const (
	MaxFileSize = 200 << 20 // bytes read from one file
	MaxText     = 4 << 20   // bytes of text kept from one file
)

// ErrNoText is returned for files that hold no text layer (a scanned PDF).
var ErrNoText = errors.New("没有可读的文字（可能是扫描件或图片）")

type reader func(path string, data []byte) (string, error)

var readers = map[string]reader{
	".txt": plain, ".text": plain, ".csv": plain, ".tsv": plain, ".log": plain,
	".json": plain, ".yaml": plain, ".yml": plain, ".xml": xmlText, ".org": plain, ".rst": plain,
	".html": htmlFile, ".htm": htmlFile, ".xhtml": htmlFile, ".mhtml": htmlFile,
	".docx": docx, ".docm": docx, ".dotx": docx, ".dotm": docx,
	".pptx": pptx, ".pptm": pptx, ".ppsx": pptx, ".potx": pptx,
	".xlsx": xlsx, ".xlsm": xlsx, ".xltx": xlsx,
	".odt": odf, ".ods": odf, ".odp": odf, ".ott": odf,
	".epub": epub,
	".rtf":  rtf,
	".doc":  doc, ".dot": doc,
	".xls": xls, ".xlt": xls,
	".ppt": ppt, ".pps": ppt,
	".pdf": pdf,
}

// Supported says whether files with this extension can be read. Markdown is
// handled by the note reader, not here.
func Supported(ext string) bool {
	_, ok := readers[strings.ToLower(ext)]
	return ok
}

// Extensions lists what Supported accepts, for help texts.
func Extensions() []string {
	var out []string
	for e := range readers {
		out = append(out, e)
	}
	return out
}

// Text reads a document and returns its text.
func Text(path string) (string, error) {
	r, ok := readers[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return "", fmt.Errorf("不支持的格式：%s", filepath.Ext(path))
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() > MaxFileSize {
		return "", fmt.Errorf("文件太大（%d MB），超过 %d MB 的不读", st.Size()>>20, MaxFileSize>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text, err := safe(r, path, data)
	if err != nil {
		return "", err
	}
	text = tidy(text)
	if strings.TrimSpace(text) == "" {
		return "", ErrNoText
	}
	return text, nil
}

// safe turns a panic in a reader (a malformed file) into an error.
func safe(r reader, path string, data []byte) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("文件读不懂：%v", p)
		}
	}()
	return r(path, data)
}

var blankLines = regexp.MustCompile(`\n{3,}`)
var trailingSpace = regexp.MustCompile(`[ \t\x{3000}]+\n`)

// tidy normalizes line ends and blank lines, drops control characters and
// caps the length.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\f':
			return '\n'
		case r == 0xFEFF || r == utf8.RuneError || r >= 0xE000 && r <= 0xF8FF:
			return -1
		case unicode.IsControl(r):
			return -1
		}
		if u, ok := cjkCompat[r]; ok {
			return u
		}
		return r
	}, s)
	s = trailingSpace.ReplaceAllString(s, "\n")
	s = blankLines.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if len(s) > MaxText {
		cut := MaxText
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "\n……（后面太长，没有收进索引）"
	}
	return s
}

// ---- character sets ----

//go:embed gbk.bin
var gbkTable []byte

// decodeGBK reads GBK / GB2312 / GB18030 two-byte text.
func decodeGBK(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 3 / 2)
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			continue
		}
		if c >= 0x81 && c <= 0xFE && i+1 < len(b) {
			t := b[i+1]
			if t >= 0x40 && t <= 0xFE && t != 0x7F {
				if r := gbkRune(c, t); r != 0 {
					sb.WriteRune(r)
					i++
					continue
				}
			}
			// four-byte GB18030 sequences: skip them whole
			if t >= 0x30 && t <= 0x39 && i+3 < len(b) {
				i += 3
				continue
			}
		}
		if c == 0x80 {
			sb.WriteRune('€')
		}
	}
	return sb.String()
}

func gbkRune(lead, trail byte) rune {
	i := (int(lead)-0x81)*191 + int(trail) - 0x40
	if i < 0 || 2*i+1 >= len(gbkTable) {
		return 0
	}
	return rune(binary.LittleEndian.Uint16(gbkTable[2*i:]))
}

// looksGBK says whether bytes that are not UTF-8 read cleanly as GBK.
func looksGBK(b []byte) bool {
	good, bad := 0, 0
	for i := 0; i < len(b) && i < 1<<16; i++ {
		c := b[i]
		if c < 0x80 {
			continue
		}
		if c >= 0x81 && c <= 0xFE && i+1 < len(b) && b[i+1] >= 0x40 && b[i+1] != 0x7F && gbkRune(c, b[i+1]) != 0 {
			good++
			i++
			continue
		}
		bad++
	}
	return good > 0 && bad*20 <= good
}

var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func decodeCP1252(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		} else {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		}
	}
	return string(utf16.Decode(u))
}

// decodeText guesses the character set of a text file: a BOM, UTF-8, GBK,
// then Windows-1252.
func decodeText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], false)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], true)
	case utf8.Valid(b):
		return string(b)
	case looksUTF16LE(b):
		return decodeUTF16(b, false)
	case looksGBK(b):
		return decodeGBK(b)
	}
	return decodeCP1252(b)
}

// looksUTF16LE: BOM-less UTF-16 from Windows (every other byte zero in ASCII).
func looksUTF16LE(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	zeros := 0
	n := len(b)
	if n > 4096 {
		n = 4096
	}
	for i := 1; i < n; i += 2 {
		if b[i] == 0 {
			zeros++
		}
	}
	return zeros*3 > n/2
}

// decodeCharset decodes bytes in a named character set, as HTML and XML
// declare them.
func decodeCharset(b []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "gbk", "gb2312", "gb18030", "x-gbk", "cp936", "936", "euc-cn", "hz-gb-2312":
		return decodeGBK(b)
	case "utf-16", "utf-16le":
		return decodeUTF16(b, false)
	case "utf-16be":
		return decodeUTF16(b, true)
	case "windows-1252", "iso-8859-1", "latin1", "cp1252", "us-ascii", "ascii":
		return decodeCP1252(b)
	case "utf-8", "utf8":
		return string(b)
	}
	return decodeText(b)
}

func plain(_ string, data []byte) (string, error) { return decodeText(data), nil }
