// Package textutil turns Chinese and English text into index terms and
// "content units" used to judge how much of a question a note covers.
//
// Chinese is cut into overlapping two-character pairs (我们哪天发版 →
// 我们 们哪 哪天 天发 发版). No dictionary is needed, so the binary has no
// dependencies, and any word of two or more characters is found by its pairs.
// English is cut into words, lower-cased, stop words dropped and suffixes
// trimmed by a small stemmer (pushing → push, releases → releas).
package textutil

import (
	"strings"
	"unicode"
)

// IsCJK reports whether r is a Chinese/Japanese ideograph or kana.
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// Normalize lower-cases s and folds full-width ASCII (ＡＢＣ１２３) to half-width.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x3000:
			r = ' '
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Run is a maximal stretch of one script inside a text.
type Run struct {
	Text string
	CJK  bool
}

// Runs splits normalized text into CJK runs and latin word runs; everything
// else (spaces, punctuation, symbols) separates runs and is dropped.
func Runs(s string) []Run {
	s = Normalize(s)
	var out []Run
	var cur []rune
	curCJK := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, Run{Text: string(cur), CJK: curCJK})
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case IsCJK(r):
			if !curCJK {
				flush()
				curCJK = true
			}
			cur = append(cur, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if curCJK {
				flush()
				curCJK = false
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Bigrams returns the overlapping character pairs of a CJK run. A one-character
// run yields that character alone.
func Bigrams(run string) []string {
	rs := []rune(run)
	if len(rs) == 1 {
		return []string{run}
	}
	out := make([]string, 0, len(rs)-1)
	for i := 0; i+1 < len(rs); i++ {
		out = append(out, string(rs[i:i+2]))
	}
	return out
}

// Terms returns the index terms of a text, with repeats (for term frequency).
func Terms(s string) []string {
	var out []string
	for _, run := range Runs(s) {
		if run.CJK {
			out = append(out, Bigrams(run.Text)...)
			continue
		}
		if t, ok := LatinTerm(run.Text); ok {
			out = append(out, t)
		}
	}
	return out
}

// LatinTerm stems a latin word and reports whether it is worth indexing.
func LatinTerm(w string) (string, bool) {
	if w == "" {
		return "", false
	}
	if isDigits(w) {
		return w, true
	}
	if len([]rune(w)) < 2 || englishStop[w] {
		return "", false
	}
	return Stem(w), true
}

func isDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// Stem trims common English suffixes so that test/tests/testing,
// push/pushes/pushing and release/released/releases meet.
func Stem(w string) string {
	n := len(w)
	switch {
	case n > 5 && strings.HasSuffix(w, "ies"):
		w = w[:n-3] + "y"
	case n > 5 && strings.HasSuffix(w, "ing"):
		w = undouble(w[:n-3])
	case n > 4 && strings.HasSuffix(w, "ed"):
		w = undouble(w[:n-2])
	case n > 4 && (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "shes") ||
		strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "xes")):
		w = w[:n-2]
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		w = w[:n-1]
	}
	if len(w) > 4 && strings.HasSuffix(w, "e") {
		w = w[:len(w)-1]
	}
	return w
}

func undouble(w string) string {
	n := len(w)
	if n > 3 && w[n-1] == w[n-2] && strings.ContainsRune("bdgmnprt", rune(w[n-1])) {
		return w[:n-1]
	}
	return w
}

// Unit is one meaningful piece of a question: a Chinese character that is not
// a function word, or an English word that is not a stop word. A note "covers"
// a unit when the question's own pairs (or word) around it occur in the note.
type Unit struct {
	Text     string   // the character, or the stemmed word
	CJK      bool     // a Chinese character
	Pairs    []string // the question's pairs that contain this character
	Isolated bool     // the character stood alone between non-CJK text
}

// Units lists the content units of a text, in order, without repeats.
func Units(s string) []Unit {
	var out []Unit
	seen := map[string]bool{}
	for _, run := range Runs(s) {
		if !run.CJK {
			t, ok := LatinTerm(run.Text)
			if !ok || seen["w:"+t] {
				continue
			}
			seen["w:"+t] = true
			out = append(out, Unit{Text: t})
			continue
		}
		rs := []rune(run.Text)
		inPhrase := stopPhraseMask(rs)
		for i, r := range rs {
			c := string(r)
			if cjkStop[r] || inPhrase[i] || seen["c:"+c] {
				continue
			}
			u := Unit{Text: c, CJK: true}
			if len(rs) == 1 {
				u.Isolated = true
				u.Pairs = []string{c}
			} else {
				if i > 0 {
					u.Pairs = append(u.Pairs, string(rs[i-1:i+1]))
				}
				if i+1 < len(rs) {
					u.Pairs = append(u.Pairs, string(rs[i:i+2]))
				}
			}
			seen["c:"+c] = true
			out = append(out, u)
		}
	}
	return out
}

// cjkStop holds function characters that carry no topic on their own:
// particles, pronouns, question words and the like.
var cjkStop = map[rune]bool{}

func init() {
	for _, r := range "的了吗呢吧啊呀么嘛哦哈呗我你您他她它们这那哪是在把被给个一都也还又很就和与或及等啥谁怎什如何请帮" {
		cjkStop[r] = true
	}
}

// cjkStopPhrases are question and filler words whose characters mean nothing
// on their own here (多少 is "how much", not 多 "many" plus 少 "few").
var cjkStopPhrases = [][]rune{}

func init() {
	for _, p := range strings.Fields("怎么样 什么样 为什么 有没有 是不是 能不能 要不要 可不可以 多少 怎样 可以 一下 一些 这样 那样 哪里 哪儿 如何 是否 能否 需不需要") {
		cjkStopPhrases = append(cjkStopPhrases, []rune(p))
	}
}

func stopPhraseMask(rs []rune) []bool {
	mask := make([]bool, len(rs))
	for _, p := range cjkStopPhrases {
		for i := 0; i+len(p) <= len(rs); i++ {
			match := true
			for j, r := range p {
				if rs[i+j] != r {
					match = false
					break
				}
			}
			if match {
				for j := range p {
					mask[i+j] = true
				}
			}
		}
	}
	return mask
}

var englishStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the is are was were be been being am to of in on at for with by from as
and or but if then than so do does did done doing have has had having i me my mine we us our ours you your yours
he him his she her it its they them their this that these those what which who whom whose when where why how
can could should would will shall may might must not no yes there here about into over under again just also
any all some such only own same very too more most other out up down off now please let tell show give get got
make know think want need use using used like way thing things something anything one ok okay hi hello thanks
thank sure yeah yep`) {
		englishStop[w] = true
	}
}

// Contains reports whether the normalized phrase occurs in the normalized text;
// latin phrases must start and end on word boundaries.
func Contains(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	start := 0
	for {
		i := strings.Index(text[start:], phrase)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(phrase)
		if boundary(text, i, phrase, true) && boundary(text, end, phrase, false) {
			return true
		}
		start = i + 1
	}
}

func boundary(text string, at int, phrase string, before bool) bool {
	var edge rune
	if before {
		edge, _ = firstRune(phrase)
	} else {
		edge = lastRune(phrase)
	}
	if IsCJK(edge) {
		return true
	}
	var nb rune
	if before {
		if at == 0 {
			return true
		}
		nb = lastRune(text[:at])
	} else {
		if at >= len(text) {
			return true
		}
		nb, _ = firstRune(text[at:])
	}
	return !(unicode.IsLetter(nb) || unicode.IsDigit(nb)) || IsCJK(nb)
}

func firstRune(s string) (rune, int) {
	for i, r := range s {
		return r, i
	}
	return 0, 0
}

func lastRune(s string) rune {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1]
}

// CJKLen counts the Chinese characters in s.
func CJKLen(s string) int {
	n := 0
	for _, r := range s {
		if IsCJK(r) {
			n++
		}
	}
	return n
}

// EstimateTokens gives a rough model-token count: one per Chinese character,
// about 1.3 per English word, plus a little for punctuation.
func EstimateTokens(s string) int {
	cjk, words, other := 0, 0, 0
	inWord := false
	for _, r := range s {
		switch {
		case IsCJK(r):
			cjk++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				words++
			}
			inWord = true
		default:
			if !unicode.IsSpace(r) {
				other++
			}
			inWord = false
		}
	}
	return cjk + words*13/10 + other/2
}
