package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	p1 = "上市公司监管档案管理内部工作规程"
	p2 = "档案应当在办结后三十日内归档。"
	en = "Quarterly review of disclosure rules"
)

func TestFormats(t *testing.T) {
	externalPDF = func(string) (string, bool) { return "", false } // only x-mem's own reader
	cases := []struct {
		file string
		want []string
		not  []string
	}{
		{"sample.docx", []string{p1, p2, en, "部门\t公司监管部"}, nil},
		{"sample.doc", []string{p1, p2, en, "公司监管部"}, nil},
		{"sample.odt", []string{p1, p2, en, "公司监管部"}, nil},
		{"sample.rtf", []string{p1, p2, en, "公司监管部"}, nil},
		{"sample.pdf", []string{p1, p2, en, "[第 1 页]"}, nil},
		{"objstm.pdf", []string{p1, p2, en}, nil},
		{"enc-rc4.pdf", []string{p1, p2, en}, nil},
		{"enc-aes128.pdf", []string{p1, p2, en}, nil},
		{"enc-aes256.pdf", []string{p1, p2, en}, nil},
		{"cid-nounicode.pdf", []string{p1, p2, en, "[第 2 页]", "抽查比例不低于百分之十"}, nil},
		{"sample.pptx", []string{"[幻灯片 1]", p1, p2, "[幻灯片 2]", "第二页标题", en, "讲稿备注"}, nil},
		{"sample.ppt", []string{p1, p2, "第二页标题", en}, nil},
		{"sample.odp", []string{p1, p2, en}, nil},
		{"sample.xlsx", []string{"[工作表 归档台账]", "事项\t期限（日）\t负责人", p2 + "\t30\t张三", "90.5", "[工作表 说明]", en}, nil},
		{"sample.xls", []string{"归档台账", p2, "30", "张三", "90.5", en}, nil},
		{"sample.ods", []string{"归档台账", p2, "张三", en}, nil},
		{"sample.epub", []string{p1, p2, en}, []string{"不该出现", "color:red"}},
		{"gbk.txt", []string{p1, p2}, nil},
		{"gbk.csv", []string{p2 + ",30"}, nil},
		{"utf16.txt", []string{p1}, nil},
		{"gbk.html", []string{p1, "部门\t公司监管部", "A & B <C>"}, []string{"不该出现", "alert"}},
		{"gbk.rtf", []string{p2, "Unicode: 中文"}, []string{"宋体"}},
	}
	for _, c := range cases {
		got, err := Text(filepath.Join("testdata", c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in:\n%s", c.file, w, got)
				break
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: should not contain %q:\n%s", c.file, w, got)
			}
		}
	}
}

func TestPasswordPDF(t *testing.T) {
	externalPDF = func(string) (string, bool) { return "", false }
	if _, err := Text("testdata/enc-userpw.pdf"); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Fatalf("want a password error, got %v", err)
	}
}

func TestBrokenFilesDoNotPanic(t *testing.T) {
	dir := t.TempDir()
	junk := []byte("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Kids [3 0 R] >> endobj\n3 0 obj << /Type /Page /Contents 4 0 R >> endobj\n4 0 obj << /Length 999 /Filter /FlateDecode >> stream\nxx")
	for _, name := range []string{"a.pdf", "a.docx", "a.doc", "a.xls", "a.ppt", "a.xlsx", "a.epub", "a.rtf"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, junk, 0o644)
		Text(p) // must return, not panic
	}
	trunc, _ := os.ReadFile("testdata/sample.pdf")
	p := filepath.Join(dir, "trunc.pdf")
	os.WriteFile(p, trunc[:len(trunc)/2], 0o644)
	Text(p)
}
