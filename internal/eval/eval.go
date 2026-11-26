// Package eval runs a regression set: questions with the answer memory should
// give. Every change to tokenizing or thresholds is checked against it.
package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"x-mem/internal/index"
	"x-mem/internal/recall"
)

// Case is one question.
type Case struct {
	Q      string   `json:"q"`
	Expect string   `json:"expect"` // direct | related | none | silent (the prompt hook injects nothing)
	Top    string   `json:"top,omitempty"`
	Not    []string `json:"not,omitempty"` // ids that must not be offered as direct
	Note   string   `json:"note,omitempty"`
}

// File is a regression set on disk.
type File struct {
	Cases []Case `json:"cases"`
}

// Load reads a regression set.
func Load(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(data, &f)
	return f, err
}

// Outcome is the result of one case.
type Outcome struct {
	Case   Case
	Got    recall.Tier
	TopID  string
	Pass   bool
	Reason string
}

// Run checks every case against the index.
func Run(ix *index.Index, f File, now time.Time) []Outcome {
	var out []Outcome
	for _, c := range f.Cases {
		res := recall.Search(ix, c.Q, recall.Options{Limit: 5, Now: now})
		o := Outcome{Case: c, Got: res.Tier}
		if len(res.Hits) > 0 {
			o.TopID = res.Hits[0].Doc.ID
		}
		o.Pass, o.Reason = judge(c, res, o.TopID)
		out = append(out, o)
	}
	return out
}

func judge(c Case, res recall.Result, top string) (bool, string) {
	switch c.Expect {
	case "silent":
		if res.Tier == recall.Direct {
			return false, "钩子会注入 " + top
		}
		return true, ""
	case "none", "related", "direct":
		if res.Tier.String() != c.Expect {
			return false, fmt.Sprintf("期望 %s，实际 %s（最高：%s）", c.Expect, res.Tier, top)
		}
	default:
		return false, "expect 只能是 direct、related、none 或 silent"
	}
	if c.Top != "" && top != c.Top {
		return false, fmt.Sprintf("期望最高的是 %s，实际是 %s", c.Top, top)
	}
	for _, h := range res.Hits {
		if h.Tier != recall.Direct {
			break
		}
		for _, bad := range c.Not {
			if h.Doc.ID == bad {
				return false, "不该出现的 " + bad + " 被当成了直接依据"
			}
		}
	}
	return true, ""
}

// Report prints the outcomes and a summary per expectation; it returns the
// number of failures.
func Report(w io.Writer, outs []Outcome) int {
	type tally struct{ pass, total int }
	groups := map[string]*tally{}
	order := []string{}
	failed := 0
	for _, o := range outs {
		g := o.Case.Expect
		if groups[g] == nil {
			groups[g] = &tally{}
			order = append(order, g)
		}
		groups[g].total++
		if o.Pass {
			groups[g].pass++
			continue
		}
		failed++
		fmt.Fprintf(w, "✗ %s\n    %s\n", o.Case.Q, o.Reason)
	}
	var parts []string
	for _, g := range order {
		parts = append(parts, fmt.Sprintf("%s %d/%d", g, groups[g].pass, groups[g].total))
	}
	fmt.Fprintf(w, "通过 %d/%d（%s）\n", len(outs)-failed, len(outs), strings.Join(parts, "，"))
	return failed
}
