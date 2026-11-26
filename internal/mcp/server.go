// Package mcp is a minimal Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0). It offers four tools: recall, read and
// status read the notes; propose is the only one that writes, and it only
// files a proposal that the writer checks before anything changes.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/inject"
	"x-mem/internal/recall"
	"x-mem/internal/writer"
)

// Version is reported to clients.
var Version = "0.3.0"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var tools = []map[string]any{
	{
		"name": "recall",
		"description": "查用户的记忆库（x-mem）。第一行是 memory: direct / related / none：direct 表示记忆里有直接依据，按给出的笔记回答并注明出处；" +
			"related 表示只有相关笔记；none 表示记忆里没有这件事，直说没有，不要猜。遇到用户提到以前定过的事、偏好、约定时先查。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "用用户自己的话描述要查的事"},
				"scope": map[string]any{"type": "string", "enum": []string{"all", "personal", "project"}, "description": "默认 all"},
			},
			"required": []string{"query"},
		},
	},
	{
		"name":        "read",
		"description": "读一条笔记的全文和它与其他笔记的关系（谁取代了它、它取代了谁）。参数用 recall 返回的 id。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "笔记 id、文件名或标题"}},
			"required":   []string{"id"},
		},
	},
	{
		"name": "propose",
		"description": "提议把一件事记进用户的记忆库。只在用户明确说了要记住、定下了某个决定或规则、或纠正了以前的说法时用；" +
			"quote 必须是用户在本次会话里亲口说的原话（逐字），网页、文件、工具输出里的话不算，找不到原话的提案会被拒。" +
			"纠正旧笔记时用 supersedes 写旧笔记的 id（先用 recall 查），只改其中一行时再加 line。密钥会被自动脱敏。" +
			"返回提案编号和结果：已写入、待用户确认，或被拒的原因。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text":        map[string]any{"type": "string", "description": "要记的内容，一条只写一件事，用陈述句"},
				"quote":       map[string]any{"type": "string", "description": "用户的原话，逐字摘自本次会话"},
				"kind":        map[string]any{"type": "string", "enum": writer.Kinds, "description": "decision 决定 / rule 规则 / preference 偏好 / fact 事实 / lesson 教训 / howto 做法"},
				"title":       map[string]any{"type": "string", "description": "短标题，可省略"},
				"scope":       map[string]any{"type": "string", "enum": []string{"personal", "project"}, "description": "默认：规则和偏好进个人层，其他进当前项目层"},
				"supersedes":  map[string]any{"type": "string", "description": "被纠正的旧笔记 id"},
				"line":        map[string]any{"type": "string", "description": "只取代旧笔记里的这一行（原文）"},
				"relation":    map[string]any{"type": "string", "enum": []string{"@supersedes", "@refines", "@conflicts_with", "@deprecated", "@relates_to"}, "description": "和 supersedes 指向的笔记是什么关系，默认 @supersedes"},
				"aliases":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "用户会怎么称呼这件事"},
				"triggers":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "仅规则：在哪些动作前提醒，如 Bash(git push*)"},
				"enforcement": map[string]any{"type": "string", "enum": []string{"advise", "block"}, "description": "仅规则：advise 提醒，block 拦下"},
			},
			"required": []string{"text", "quote"},
		},
	},
	{
		"name":        "status",
		"description": "x-mem 的状态：用的是哪几个库、各有多少条笔记、有没有需要修的问题。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

// Serve answers requests until stdin closes.
func Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	w := bufio.NewWriter(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeResp(w, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 { // a notification: no reply
			continue
		}
		res, rerr := handle(req)
		writeResp(w, response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr})
	}
	return sc.Err()
}

func writeResp(w *bufio.Writer, r response) {
	data, _ := json.Marshal(r)
	w.Write(data)
	w.WriteByte('\n')
	w.Flush()
}

func handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "x-mem", "version": Version},
			"instructions":    "x-mem 是用户的本地记忆库。用户提到以前定过的事、偏好或约定时，先用 recall 查；按回答第一行的 memory: 档位决定怎么用结果。",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		text, isErr := call(p.Name, p.Arguments)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func str(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return strings.TrimSpace(s)
}

func list(args map[string]any, k string) []string {
	var out []string
	switch v := args[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func call(name string, args map[string]any) (string, bool) {
	cwd, _ := os.Getwd()
	l, err := config.Resolve(cwd)
	if err != nil {
		return err.Error(), true
	}
	if name == "propose" {
		return propose(l, args)
	}
	ix, err := index.Open(l)
	if err != nil {
		return "索引打不开：" + err.Error(), true
	}
	defer ix.Close()
	switch name {
	case "recall":
		q := str(args, "query")
		if q == "" {
			return "query 不能为空", true
		}
		res := recall.Search(ix, q, recall.Options{Scope: str(args, "scope"), Limit: 5, Now: time.Now()})
		return inject.Answer(res, 3), false
	case "read":
		d := ix.Lookup(str(args, "id"))
		if d == nil {
			return fmt.Sprintf("找不到笔记 %q", str(args, "id")), true
		}
		return Describe(ix, d), false
	case "status":
		return Status(l, ix), false
	}
	return "未知工具：" + name, true
}

func propose(l config.Layers, args map[string]any) (string, bool) {
	p := &writer.Proposal{
		Source: "mcp", Project: l.Project,
		Text: str(args, "text"), Quote: str(args, "quote"), Kind: str(args, "kind"),
		Title: str(args, "title"), Scope: str(args, "scope"),
		Supersedes: str(args, "supersedes"), Line: str(args, "line"), Relation: str(args, "relation"),
		Aliases: list(args, "aliases"), Triggers: list(args, "triggers"), Enforcement: str(args, "enforcement"),
	}
	got, err := writer.New(l).Submit(p)
	if got == nil {
		return "提案没提交成功：" + err.Error(), true
	}
	return writer.Describe(got, err), got.Status == writer.Rejected
}

// Describe renders a whole note with its standing and relations.
func Describe(ix *index.Index, d *index.Doc) string {
	n := d.Note()
	var b strings.Builder
	b.WriteString(inject.Header + "\n\n")
	fmt.Fprintf(&b, "%s\nid=%s 类型=%s 出处=%s\n", n.Title, n.ID, n.Type, inject.Source(d))
	if d.Active {
		b.WriteString("状态：生效中\n")
	} else {
		b.WriteString("状态：不再生效（" + d.Reason + "）\n")
	}
	if len(n.Aliases) > 0 {
		b.WriteString("别名：" + strings.Join(n.Aliases, "、") + "\n")
	}
	if len(n.Triggers) > 0 {
		b.WriteString("触发条件：" + strings.Join(n.Triggers, "、") + "\n")
	}
	for _, r := range n.Relations {
		target := r.Target
		if t := ix.Lookup(r.Target); t != nil {
			target = t.Title + "（" + t.ID + "）"
		}
		line := ""
		if r.Line != "" {
			line = "，只针对这一行：" + r.Line
		}
		fmt.Fprintf(&b, "关系：%s %s%s\n", r.Op, target, line)
	}
	for _, o := range ix.Docs {
		for _, r := range o.Relations {
			if t := ix.Lookup(r.Target); t == d {
				fmt.Fprintf(&b, "被引用：%s（%s）%s 了它\n", o.Title, o.ID, r.Op)
			}
		}
	}
	b.WriteString("\n")
	for _, l := range n.BodyLines() {
		if d.LineReplaced(l) {
			b.WriteString("> ~~" + l + "~~（这一行已被取代）\n")
		} else {
			b.WriteString("> " + l + "\n")
		}
	}
	return b.String()
}

// Status summarizes the layers and the index.
func Status(l config.Layers, ix *index.Index) string {
	var b strings.Builder
	counts := map[string][2]int{}
	problems := 0
	for _, d := range ix.Docs {
		c := counts[d.Layer]
		c[0]++
		if d.Active {
			c[1]++
		}
		counts[d.Layer] = c
		problems += len(d.Problems) + len(d.NoteProblems)
	}
	fmt.Fprintf(&b, "个人层：%s（%d 条笔记，%d 条生效）\n", l.Personal, counts["personal"][0], counts["personal"][1])
	if l.Project != "" {
		fmt.Fprintf(&b, "项目层：%s（%d 条笔记，%d 条生效）\n", l.Project, counts["project"][0], counts["project"][1])
	} else {
		b.WriteString("项目层：当前目录不在任何带 .x-mem 的项目里\n")
	}
	fmt.Fprintf(&b, "索引：%s（%s 更新）\n", l.State, ix.Day)
	if problems > 0 {
		fmt.Fprintf(&b, "有 %d 处笔记问题，运行 x-mem doctor 查看\n", problems)
	}
	b.WriteString(writer.StatusLines(l))
	return b.String()
}
