// Package redact replaces secrets in text before it can become a proposal,
// a note or a file name. It errs on the side of replacing: a false positive
// costs a word in a note, a miss puts a password into git.
package redact

import (
	"regexp"
	"strings"
	"unicode"
)

// Mask is what a secret is replaced with.
const Mask = "[已脱敏]"

// Finding says what kind of secret was replaced.
type Finding struct {
	Kind string
}

type rule struct {
	kind string
	re   *regexp.Regexp
	// group, when > 0, is the submatch that holds the secret; the rest of the
	// match (a label such as "密码是") is kept.
	group int
	// skip, when set, leaves a match alone (e.g. "token: 1500").
	skip func(secret string) bool
}

var rules = []rule{
	{"私钥", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), 0, nil},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0, nil},
	{"webhook", regexp.MustCompile(`https?://hooks\.slack\.com/[^\s"'<>）)]+`), 0, nil},
	{"webhook", regexp.MustCompile(`https?://(?:discord(?:app)?\.com|canary\.discord\.com)/api/webhooks/[^\s"'<>）)]+`), 0, nil},
	{"webhook", regexp.MustCompile(`https?://oapi\.dingtalk\.com/robot/send\?[^\s"'<>）)]+`), 0, nil},
	{"webhook", regexp.MustCompile(`https?://qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?[^\s"'<>）)]+`), 0, nil},
	{"webhook", regexp.MustCompile(`https?://open\.feishu\.cn/open-apis/bot/v2/hook/[^\s"'<>）)]+`), 0, nil},
	{"URL 凭据", regexp.MustCompile(`(?i)([?&](?:key|api_?key|access_?token|token|secret|sig|signature|password|pwd)=)([^&\s"'<>]+)`), 2, nil},
	{"连接串密码", regexp.MustCompile(`(?i)\b((?:mysql|postgres(?:ql)?|mongodb(?:\+srv)?|redis|amqp|mssql|oracle|ftp|sftp|https?)://[^:/\s@]+:)([^@\s/]+)(@)`), 2, nil},
	{"密钥", regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-(?:proj-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|xox[abposr]-[A-Za-z0-9-]{10,}|hf_[A-Za-z0-9]{20,}|AIza[0-9A-Za-z_-]{30,}|glpat-[A-Za-z0-9_-]{20,}|npm_[A-Za-z0-9]{30,}|rk_live_[A-Za-z0-9]{16,}|sk_live_[A-Za-z0-9]{16,})`), 0, nil},
	// Labelled assignments, Chinese and English: 密码是 X、口令为 X、API_KEY=X、token: X.
	{"带标签的密钥", regexp.MustCompile(`(?i)((?:密码|口令|密钥|秘钥|令牌|私钥|验证码|授权码|\bpassword|\bpasswd|\bpassphrase|\bsecret|\btoken|\bapi[_-]?key|\baccess[_-]?key|\bsecret[_-]?key|\bclient[_-]?secret|\bauth[_-]?token)\s*(?:是|为|：|:|=|＝|\bis\b)\s*["'“‘「]?)([^\s"'”’」，。；;,、]{4,})`), 2, plainNumber},
}

// plainNumber is a short all-digit value such as a budget or a port.
func plainNumber(s string) bool {
	if len(s) >= 6 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// highEntropy finds long mixed letter-and-digit runs (16+), the shape of
// generated keys. Pure hex (hashes, commit ids) is left alone.
var highEntropy = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{16,}`)

// Text replaces every secret it recognises and reports what it found.
func Text(s string) (string, []Finding) {
	var found []Finding
	for _, r := range rules {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			if strings.Contains(m, Mask) {
				return m
			}
			sub := r.re.FindStringSubmatch(m)
			if r.skip != nil && len(sub) > r.group && r.skip(sub[r.group]) {
				return m
			}
			found = append(found, Finding{r.kind})
			if r.group == 0 {
				return Mask
			}
			if len(sub) <= r.group || sub[r.group] == "" {
				return Mask
			}
			var b strings.Builder
			for i := 1; i < len(sub); i++ {
				if i == r.group {
					b.WriteString(Mask)
				} else {
					b.WriteString(sub[i])
				}
			}
			return b.String()
		})
	}
	s = highEntropy.ReplaceAllStringFunc(s, func(m string) string {
		if !looksGenerated(m) {
			return m
		}
		found = append(found, Finding{"高熵串"})
		return Mask
	})
	return s, found
}

// looksGenerated tells a key from an ordinary long word, path piece or hash.
func looksGenerated(s string) bool {
	core := strings.Trim(s, "-_=/+")
	if len(core) < 16 {
		return false
	}
	var upper, lower, digit, hex int
	for _, r := range core {
		switch {
		case unicode.IsUpper(r):
			upper++
		case unicode.IsLower(r):
			lower++
		case unicode.IsDigit(r):
			digit++
		}
		if strings.ContainsRune("0123456789abcdefABCDEF", r) {
			hex++
		}
	}
	if hex == len(core) { // a hash or commit id
		return false
	}
	if strings.ContainsAny(core, "/") && strings.Count(core, "/") > 1 { // a path
		return false
	}
	letters := upper + lower
	if digit == 0 || letters == 0 {
		return false
	}
	// snake_case or kebab-case identifiers made of words are not secrets.
	if strings.ContainsAny(core, "_-") {
		words := strings.FieldsFunc(core, func(r rune) bool { return r == '_' || r == '-' })
		allWords := true
		for _, w := range words {
			if len(w) > 12 || (hasDigit(w) && hasLetter(w) && len(w) >= 8) {
				allWords = false
				break
			}
		}
		if allWords {
			return false
		}
	}
	// Mixed case plus digits, or many digits among letters.
	return (upper > 0 && lower > 0) || digit*4 >= len(core)
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
