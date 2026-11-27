package extract

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// The system's PDF readers, used when x-mem's own reader gets little out of
// a file: PDFKit on macOS (through osascript, no install needed), and
// pdftotext where it is installed.

const pdfKitScript = `ObjC.import('PDFKit');
function run(argv) {
  var doc = $.PDFDocument.alloc.initWithURL($.NSURL.fileURLWithPath(argv[0]));
  if (!doc || doc.isNil() || doc.isLocked) return '';
  var out = [];
  for (var i = 0; i < doc.pageCount; i++) {
    var s = doc.pageAtIndex(i).string;
    out.push('[第 ' + (i + 1) + ' 页]\n' + ((s && !s.isNil()) ? s.js : ''));
  }
  return out.join('\n\n');
}`

var externalPDF = func(path string) (string, bool) {
	run := func(name string, args ...string) (string, bool) {
		bin, err := exec.LookPath(name)
		if err != nil {
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			return "", false
		}
		return string(out), true
	}
	if runtime.GOOS == "darwin" {
		if s, ok := run("osascript", "-l", "JavaScript", "-e", pdfKitScript, path); ok {
			return s, true
		}
	}
	if s, ok := run("pdftotext", "-q", "-enc", "UTF-8", path, "-"); ok {
		return strings.ReplaceAll(s, "\f", "\n\n"), true
	}
	return "", false
}
