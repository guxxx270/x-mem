package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"x-mem/internal/config"
)

// Describe says what became of a proposal, for the propose tool and the
// command line.
func Describe(p *Proposal, err error) string {
	var b strings.Builder
	switch p.Status {
	case Applied:
		fmt.Fprintf(&b, "已写入：%s\n文件：%s\n撤回：x-mem undo %s\n", p.Title, p.Note, p.Run)
	case Review:
		fmt.Fprintf(&b, "待用户确认（提案 %s）：%s\n原因：%s\n确认：x-mem inbox approve %s；不要：x-mem inbox reject %s\n",
			p.ID, p.Title, p.Review, p.ID, p.ID)
	case Queued:
		fmt.Fprintf(&b, "已排队（提案 %s）：%s\n", p.ID, p.Title)
		if err != nil {
			fmt.Fprintf(&b, "这次没写成：%v\n下次写入时会重试；也可以运行 x-mem writer --once\n", err)
		} else {
			b.WriteString("另一个写入正在进行，稍后会自动写入\n")
		}
	case Rejected:
		fmt.Fprintf(&b, "没有写入（提案 %s）：\n", p.ID)
		for _, pr := range p.Problems {
			b.WriteString("- " + pr + "\n")
		}
	}
	if len(p.Redacted) > 0 {
		fmt.Fprintf(&b, "已脱敏：%s\n", strings.Join(p.Redacted, "、"))
	}
	return b.String()
}

// StatusLines summarise the write side for `x-mem status` and the status
// tool.
func StatusLines(l config.Layers) string {
	var b strings.Builder
	in := NewInbox(l.State)
	review, queued := len(in.List(Review)), len(in.List(Queued))
	switch {
	case review+queued == 0:
		b.WriteString("待处理提案：没有\n")
	default:
		fmt.Fprintf(&b, "待处理提案：%d 条等你确认，%d 条排队（x-mem inbox）\n", review, queued)
	}
	if runs := Runs(l.State); len(runs) > 0 {
		r := runs[0]
		state := ""
		if r.Undone {
			state = "，已撤回"
		}
		fmt.Fprintf(&b, "最近一次写入：%s %s（%s%s）\n", r.At.Format("2006-01-02 15:04"), r.Title, r.ID, state)
	}
	if data, err := os.ReadFile(filepath.Join(l.State, "capture.json")); err == nil && strings.Contains(string(data), `"enabled": true`) {
		b.WriteString("会话采集：开（x-mem capture off 关闭）\n")
	} else {
		b.WriteString("会话采集：关（x-mem capture on 打开）\n")
	}
	return b.String()
}
