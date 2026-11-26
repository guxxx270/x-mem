// Package recall answers a question from the index and decides how sure the
// answer is: direct (a note states it), related (only nearby notes) or none
// (nothing in memory). Hooks inject only direct hits; everything else stays
// silent.
package recall

import (
	"math"
	"sort"
	"strings"
	"time"

	"x-mem/internal/index"
	"x-mem/internal/textutil"
)

// Tier says how well memory answers a question.
type Tier int

const (
	None Tier = iota
	Related
	Direct
)

func (t Tier) String() string {
	switch t {
	case Direct:
		return "direct"
	case Related:
		return "related"
	}
	return "none"
}

// Thresholds. They start from the values that worked in 旧记忆库 and are tuned
// against the regression set in testdata/.
const (
	directCoverage   = 0.6 // share of the question's content units a note covers
	directMinCovered = 2   // and at least this many units
	titleCoverage    = 0.8 // or: share of the note's title found in the question
	titleMinCovered  = 3   // with at least this many title units
	relatedCoverage  = 0.3
	k1, b            = 1.2, 0.75
)

// Hit is one candidate note with the evidence for its tier.
type Hit struct {
	Doc          *index.Doc
	Tier         Tier
	Score        float64
	BM25         float64
	Coverage     float64
	Covered      []string // the question's units this note covers
	Units        int      // content units in the question
	TitleCov     float64
	TitleCovered int
	Phrase       string // an alias or title found verbatim in the question
	Why          string // one-line reason for the tier
}

// Options narrows a search.
type Options struct {
	Scope string // "", "all", "personal" or "project"
	Limit int
	Now   time.Time
}

// Result is the answer to one question.
type Result struct {
	Query string
	Tier  Tier
	Units []textutil.Unit
	Hits  []Hit
}

// Search ranks the notes in force for a question.
func Search(ix *index.Index, query string, opt Options) Result {
	if opt.Limit <= 0 {
		opt.Limit = 5
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	res := Result{Query: query}
	qn := textutil.Normalize(query)
	units := textutil.Units(query)
	res.Units = units
	qterms := map[string]int{}
	for _, t := range textutil.Terms(query) {
		qterms[t]++
	}
	qchars := map[string]bool{}
	for _, u := range units {
		if u.CJK {
			qchars[u.Text] = true
		}
	}
	// Read the postings of every term the question can match on.
	need := map[string]bool{}
	for t := range qterms {
		need[t] = true
	}
	for _, u := range units {
		if !u.CJK {
			need[u.Text] = true
		}
		for _, p := range u.Pairs {
			need[p] = true
		}
	}
	tf := make(map[string]map[int]float32, len(need))
	cand := map[int]bool{}
	for t := range need {
		ps := ix.Postings(t)
		if len(ps) == 0 {
			continue
		}
		m := make(map[int]float32, len(ps))
		for _, p := range ps {
			m[p.Doc] = p.TF
			if qterms[t] > 0 {
				cand[p.Doc] = true
			}
		}
		tf[t] = m
	}
	phrase := map[int]string{}
	for i, d := range ix.Docs {
		if !d.Active {
			continue
		}
		for _, p := range d.Phrases {
			if textutil.Contains(qn, p) {
				phrase[i] = p
				cand[i] = true
				break
			}
		}
	}
	has := func(t string, i int) bool { return tf[t][i] > 0 }

	var hits []Hit
	for i := range cand {
		d := ix.Docs[i]
		if !d.Active || !inScope(d, opt.Scope) {
			continue
		}
		h := Hit{Doc: d, Units: len(units), Phrase: phrase[i]}
		for t := range qterms {
			f := float64(tf[t][i])
			if f == 0 {
				continue
			}
			norm := f * (k1 + 1) / (f + k1*(1-b+b*float64(d.Len)/math.Max(ix.AvgLen, 1)))
			h.BM25 += ix.IDF(t) * norm
		}
		if h.BM25 == 0 && h.Phrase == "" {
			continue
		}
		for _, u := range units {
			if covers(d, i, u, has) {
				h.Covered = append(h.Covered, u.Text)
			}
		}
		if len(units) > 0 {
			h.Coverage = float64(len(h.Covered)) / float64(len(units))
		}
		for _, u := range d.TitleUnits {
			if inQuestion(u, qterms, qchars) {
				h.TitleCovered++
			}
		}
		if len(d.TitleUnits) > 0 {
			h.TitleCov = float64(h.TitleCovered) / float64(len(d.TitleUnits))
		}
		h.Tier, h.Why = decide(h)
		h.Score = h.BM25 * (0.5 + h.Coverage) * recency(d.Updated, opt.Now)
		if h.Phrase != "" {
			h.Score += 100
		}
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Tier != hits[j].Tier {
			return hits[i].Tier > hits[j].Tier
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Doc.Rel < hits[j].Doc.Rel
	})
	if len(hits) > opt.Limit {
		hits = hits[:opt.Limit]
	}
	res.Hits = hits
	if len(hits) > 0 {
		res.Tier = hits[0].Tier
	}
	return res
}

func decide(h Hit) (Tier, string) {
	n := len(h.Covered)
	switch {
	case h.Phrase != "":
		return Direct, "问题里直接提到了「" + h.Phrase + "」"
	case h.Coverage >= directCoverage && n >= directMinCovered:
		return Direct, "问题的要点大部分都在这条笔记里"
	case h.TitleCov >= titleCoverage && h.TitleCovered >= titleMinCovered:
		return Direct, "问题里完整提到了这条笔记的标题"
	case h.Coverage >= relatedCoverage && n >= 1:
		return Related, "只覆盖了问题的一部分要点"
	}
	return None, "和问题只有零星的字词重合"
}

func covers(d *index.Doc, i int, u textutil.Unit, has func(string, int) bool) bool {
	if !u.CJK {
		return has(u.Text, i)
	}
	for _, p := range u.Pairs {
		if has(p, i) {
			return true
		}
	}
	return u.Isolated && strings.Contains(d.Chars, u.Text)
}

func inQuestion(u textutil.Unit, qterms map[string]int, qchars map[string]bool) bool {
	if !u.CJK {
		return qterms[u.Text] > 0
	}
	for _, p := range u.Pairs {
		if qterms[p] > 0 {
			return true
		}
	}
	return u.Isolated && qchars[u.Text]
}

func inScope(d *index.Doc, scope string) bool {
	return scope == "" || scope == "all" || d.Layer == scope
}

// recency gives notes changed in the last two months a small lift.
func recency(day string, now time.Time) float64 {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 1
	}
	age := now.Sub(t).Hours() / 24
	if age < 0 {
		age = 0
	}
	return 1 + 0.2*math.Exp(-age/60)
}

// Summary is the first in-force line of a note, cut to max runes.
func Summary(d *index.Doc, max int) string {
	return cut(strings.TrimSuffix(d.Summary, "……"), max)
}

func cut(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "……"
}
