package capture

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"x-mem/internal/textutil"
)

// Signal words: a sentence with one of these may be worth keeping.
var cnSignals = []string{
	"决定", "定了", "定下", "以后", "今后", "从今天起", "从现在起", "往后", "改成", "改为", "改到", "换成",
	"一律", "统一", "必须", "不要", "不许", "不准", "禁止", "别再", "不再", "记住", "记一下", "记下来",
	"约定", "规定", "原因是", "踩坑", "教训", "下次", "务必", "千万", "默认用", "优先用",
	"偏好", "习惯", "喜欢用", "不喜欢", "我们用", "统一用",
}

// English signal words, as in agentcairn.
var enSignals = []string{
	"decided", "decision", "always", "never", "instead", "from now on", "going forward", "remember",
	"don't", "do not", "must", "prefer", "convention", "the reason is", "lesson", "gotcha", "switch to",
}

// Short replies that are never memory.
var acks = map[string]bool{
	"好": true, "好的": true, "好滴": true, "好嘞": true, "嗯": true, "嗯嗯": true, "继续": true, "可以": true,
	"行": true, "对": true, "对的": true, "是的": true, "是": true, "没问题": true, "谢谢": true, "收到": true,
	"ok": true, "okay": true, "yes": true, "no": true, "thanks": true, "go": true, "go on": true,
	"continue": true, "sure": true, "lgtm": true, "继续吧": true, "开始吧": true, "好的继续": true,
}

var (
	privateRe  = regexp.MustCompile(`(?s)<private>.*?</private>`)
	sentenceRe = regexp.MustCompile(`[^。！？!?\n；;]+[。！？!?；;]?`)
	fenceRe    = regexp.MustCompile("```")
)

// OptOut in a message keeps the whole session out of memory.
const OptOut = "#不记"

// Candidate is one sentence the user said that may be worth keeping.
type Candidate struct {
	Sentence string
	Message  string // the whole message it came from
	Context  string // the assistant's reply just before
	Session  string
	Cwd      string
	Time     string
}

// pasted is a message that is mostly something the user pasted: code, a
// log, a long document.
func pasted(msg string) bool {
	if fenceRe.MatchString(msg) || utf8.RuneCountInString(msg) > 1200 {
		return true
	}
	lines := strings.Split(msg, "\n")
	if len(lines) > 15 {
		return true
	}
	code := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "File \"") || strings.HasPrefix(t, "$ ") ||
			strings.HasSuffix(t, "{") || strings.HasSuffix(t, ";") || strings.HasPrefix(t, "Traceback") {
			code++
		}
	}
	return code >= 3
}

// Candidates picks the sentences of one user message worth a closer look.
// Length is counted in characters, not spaces: agentcairn counted words by
// spaces, so a whole Chinese sentence was one "word" and got dropped.
func Candidates(msg string) []string {
	msg = privateRe.ReplaceAllString(msg, " ")
	if pasted(msg) || strings.Contains(msg, OptOut) {
		return nil
	}
	whole := strings.ToLower(strings.TrimSpace(strings.Trim(msg, "。！!.~～ ")))
	if acks[whole] {
		return nil
	}
	var out []string
	for _, s := range sentenceRe.FindAllString(msg, -1) {
		s = strings.TrimSpace(s)
		if !longEnough(s) || acks[strings.ToLower(strings.Trim(s, "。！!.~～，, "))] {
			continue
		}
		if hasSignal(s) {
			out = append(out, s)
		}
	}
	return out
}

func longEnough(s string) bool {
	cjk := textutil.CJKLen(s)
	if cjk >= 6 {
		return true
	}
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return cjk == 0 && len(words) >= 5 || cjk > 0 && cjk+len(words) >= 7
}

func hasSignal(s string) bool {
	for _, w := range cnSignals {
		if strings.Contains(s, w) {
			return true
		}
	}
	low := " " + strings.ToLower(s) + " "
	for _, w := range enSignals {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// guessKind is used when no host model is there to classify a sentence.
func guessKind(s string) string {
	for _, w := range []string{"以后", "今后", "一律", "统一", "必须", "不要", "不许", "不准", "禁止", "别再", "务必", "always", "never", "must", "don't", "do not"} {
		if strings.Contains(strings.ToLower(s), w) {
			return "rule"
		}
	}
	for _, w := range []string{"偏好", "习惯", "喜欢", "prefer"} {
		if strings.Contains(strings.ToLower(s), w) {
			return "preference"
		}
	}
	for _, w := range []string{"踩坑", "教训", "lesson", "gotcha"} {
		if strings.Contains(strings.ToLower(s), w) {
			return "lesson"
		}
	}
	return "decision"
}
