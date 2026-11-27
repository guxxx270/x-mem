#!/usr/bin/env python3
"""Builds the sample documents the extract tests read.

Needs python-docx, python-pptx, openpyxl, reportlab, LibreOffice (soffice)
and qpdf. The outputs are committed, so the tests do not need any of these.

    python3 internal/extract/testdata/gen.py
"""
import os, subprocess, shutil, tempfile, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
os.chdir(HERE)

P1 = "上市公司监管档案管理内部工作规程"
P2 = "档案应当在办结后三十日内归档。"
EN = "Quarterly review of disclosure rules"

def soffice(src, fmt, out_name):
    tmp = tempfile.mkdtemp()
    subprocess.run(["soffice", "--headless", "--convert-to", fmt, "--outdir", tmp, src],
                   check=True, capture_output=True)
    produced = [f for f in os.listdir(tmp)]
    shutil.move(os.path.join(tmp, produced[0]), out_name)
    shutil.rmtree(tmp)

# Word
import docx
d = docx.Document()
d.add_heading(P1, 1)
d.add_paragraph(P2)
d.add_paragraph(EN)
t = d.add_table(rows=2, cols=2)
t.cell(0, 0).text = "部门"; t.cell(0, 1).text = "公司监管部"
t.cell(1, 0).text = "期限"; t.cell(1, 1).text = "三十日"
d.save("sample.docx")
soffice("sample.docx", "doc:MS Word 97", "sample.doc")
soffice("sample.docx", "odt", "sample.odt")
soffice("sample.docx", "rtf", "sample.rtf")
soffice("sample.docx", "pdf", "sample.pdf")

# PowerPoint
from pptx import Presentation
pr = Presentation()
s = pr.slides.add_slide(pr.slide_layouts[1])
s.shapes.title.text = P1
s.placeholders[1].text = P2
s.notes_slide.notes_text_frame.text = "讲稿备注：先讲归档期限"
s2 = pr.slides.add_slide(pr.slide_layouts[1])
s2.shapes.title.text = "第二页标题"
s2.placeholders[1].text = EN
pr.save("sample.pptx")
soffice("sample.pptx", "ppt", "sample.ppt")
soffice("sample.pptx", "odp", "sample.odp")

# Excel
import openpyxl
wb = openpyxl.Workbook()
ws = wb.active
ws.title = "归档台账"
ws.append(["事项", "期限（日）", "负责人"])
ws.append([P2, 30, "张三"])
ws.append(["季度检查", 90.5, "李四"])
wb.create_sheet("说明").append([EN])
wb.save("sample.xlsx")
soffice("sample.xlsx", "xls:MS Excel 97", "sample.xls")
soffice("sample.xlsx", "ods", "sample.ods")

# PDF with a CJK font and no ToUnicode (UniGB-UCS2-H)
from reportlab.pdfgen import canvas
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.cidfonts import UnicodeCIDFont
pdfmetrics.registerFont(UnicodeCIDFont("STSong-Light"))
c = canvas.Canvas("cid-nounicode.pdf")
c.setFont("STSong-Light", 14)
c.drawString(72, 760, P1)
c.drawString(72, 740, P2)
c.setFont("Helvetica", 12)
c.drawString(72, 700, EN)
c.showPage()
c.setFont("STSong-Light", 14)
c.drawString(72, 760, "第二页：抽查比例不低于百分之十。")
c.save()

# Encrypted and compressed variants of the LibreOffice PDF
subprocess.run(["qpdf", "--allow-weak-crypto", "--encrypt", "", "owner-secret", "128", "--use-aes=n", "--print=none", "--", "sample.pdf", "enc-rc4.pdf"], check=True)
subprocess.run(["qpdf", "--allow-weak-crypto", "--encrypt", "", "owner-secret", "128", "--use-aes=y", "--", "sample.pdf", "enc-aes128.pdf"], check=True)
subprocess.run(["qpdf", "--encrypt", "", "owner-secret", "256", "--", "sample.pdf", "enc-aes256.pdf"], check=True)
subprocess.run(["qpdf", "--encrypt", "user-pw", "owner-secret", "256", "--", "sample.pdf", "enc-userpw.pdf"], check=True)
subprocess.run(["qpdf", "--object-streams=generate", "--compress-streams=y", "sample.pdf", "objstm.pdf"], check=True)

# EPUB
with zipfile.ZipFile("sample.epub", "w") as z:
    z.writestr("mimetype", "application/epub+zip", compress_type=zipfile.ZIP_STORED)
    z.writestr("META-INF/container.xml", '<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
    z.writestr("OEBPS/content.opf", '<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/><item id="c2" href="text/ch2.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>')
    z.writestr("OEBPS/ch1.xhtml", f'<html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title><style>p{{color:red}}</style></head><body><h1>{P1}</h1><p>{P2}</p></body></html>')
    z.writestr("OEBPS/text/ch2.xhtml", f'<html><body><p>{EN}</p><script>var x="不该出现";</script></body></html>')

# GBK text, CSV, HTML and RTF
with open("gbk.txt", "wb") as f: f.write((P1 + "\n" + P2 + "\n").encode("gbk"))
with open("gbk.csv", "wb") as f: f.write(("事项,期限\n" + P2 + ",30\n").encode("gbk"))
with open("utf16.txt", "wb") as f: f.write(("﻿" + P1 + "\n").encode("utf-16-le"))
with open("gbk.html", "wb") as f:
    f.write(('<html><head><meta charset="gb2312"><title>标题</title><script>alert("不该出现")</script></head>'
             f'<body><p>{P1}</p><table><tr><td>部门</td><td>公司监管部</td></tr></table><p>A &amp; B&nbsp;&lt;C&gt;</p></body></html>').encode("gbk"))
rtf_body = "".join("\\'%02x" % b for b in P2.encode("gbk"))
with open("gbk.rtf", "w") as f:
    f.write("{\\rtf1\\ansi\\ansicpg936{\\fonttbl{\\f0\\fnil\\fcharset134 \\'cb\\'ce\\'cc\\'e5;}}"
            "{\\*\\generator Test;}\\f0 " + rtf_body + "\\par Unicode: \\u20013?\\u25991?\\par}")
print("done")
