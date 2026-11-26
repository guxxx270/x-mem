package note

import (
	"reflect"
	"strings"
	"testing"
)

const full = `---
id: 01J8K3Q7
title: 发版日
type: decision          # decision | rule
aliases: [发版, "哪天发版", release day]
status: active
valid_from: 2026-11-04
relations:
  - op: "@supersedes"
    target: 01J8K1A2
    line: "我们每周五下午发版"
  - op: "@relates_to"
    target: ci
source:
  quote: "发版改到周二上午吧，测试周一才跑完"
  session: claude:4f2a9c1e
triggers:
  - "Bash(git push*)"
custom_key: kept as is
---
# 发版日

发版改到每周二上午。
`

func TestParseFull(t *testing.T) {
	n := Parse(full, "notes/发版日-01J8K.md")
	if n.ID != "01J8K3Q7" || n.Title != "发版日" || n.Type != "decision" || n.Status != "active" {
		t.Fatalf("basic fields wrong: %+v", n)
	}
	if !reflect.DeepEqual(n.Aliases, []string{"发版", "哪天发版", "release day"}) {
		t.Fatalf("aliases = %#v", n.Aliases)
	}
	if len(n.Relations) != 2 || n.Relations[0].Op != "@supersedes" || n.Relations[0].Line != "我们每周五下午发版" {
		t.Fatalf("relations = %+v", n.Relations)
	}
	if n.SourceQuote != "发版改到周二上午吧，测试周一才跑完" {
		t.Fatalf("quote = %q", n.SourceQuote)
	}
	if !reflect.DeepEqual(n.Triggers, []string{"Bash(git push*)"}) {
		t.Fatalf("triggers = %#v", n.Triggers)
	}
	if got := n.BodyLines(); !reflect.DeepEqual(got, []string{"发版改到每周二上午。"}) {
		t.Fatalf("body lines = %#v", got)
	}
	if len(n.Problems) != 0 {
		t.Fatalf("unexpected problems %v", n.Problems)
	}
}

func TestUnknownRelationIsAProblemNotSilence(t *testing.T) {
	n := Parse("---\nrelations:\n  - op: supersedes\n    target: x\n---\nbody", "a.md")
	if len(n.Relations) != 0 || len(n.Problems) != 1 || !strings.Contains(n.Problems[0], "supersedes") {
		t.Fatalf("a misspelt operator must be reported: %+v", n)
	}
}

func TestFallbacks(t *testing.T) {
	n := Parse("# 订单服务数据库\n\n用 PostgreSQL。", "notes/db.md")
	if n.Title != "订单服务数据库" || n.ID != "file:notes/db" || n.Status != "active" {
		t.Fatalf("fallbacks wrong: %+v", n)
	}
	n = Parse("just text", "notes/plain.md")
	if n.Title != "plain" {
		t.Fatalf("title should fall back to the file name, got %q", n.Title)
	}
}

func TestCRLFAndBlockListAtSameIndent(t *testing.T) {
	src := "---\r\ntitle: x\r\naliases:\r\n- 甲\r\n- 乙\r\n---\r\nbody\r\n"
	n := Parse(src, "x.md")
	if !reflect.DeepEqual(n.Aliases, []string{"甲", "乙"}) || n.Body != "body" {
		t.Fatalf("parse = %+v", n)
	}
}
