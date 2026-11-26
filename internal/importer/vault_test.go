package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"x-mem/internal/note"
)

func write(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const prefs = `---
title: "用户偏好与工作约束"
status: active
---

# 用户偏好与工作约束

- **日常语言**：日常统一使用**中文**沟通。
- **知识库整理习惯**：
  - 不要移动剪藏（Clippings）原件，复制到正式位置再用。
  - 名称要一眼看出用途。
`

func TestVaultImport(t *testing.T) {
	v := t.TempDir()
	write(t, v, "Agent第二大脑.md", "# 入口\n")
	write(t, v, "02 经验与方法/Agent/用户偏好与工作约束.md", prefs)
	write(t, v, "02 经验与方法/Agent/镜像.md", "---\ntitle: 国内网络镜像\nhow: \"pip 走清华源\"\n---\n# 国内网络镜像\n\n- pip 用清华源\n")
	write(t, v, "02 经验与方法/从 Codex 导入/01_x.md", "---\ntitle: \"Codex经验：nginx\"\nsource: codex-memory\n---\n# Codex经验：nginx\n\nport conflict\n")
	write(t, v, "02 经验与方法/准经验/准经验入口.md", "# 入口\n")
	write(t, v, "05 时间日志/2026-11/25｜某天.md", "# 日志\n")
	write(t, v, "90 系统文件/canonical_sources.psv", strings.Join([]string{
		"# id|别名|路径|锚句",
		"Q02|剪藏;剪藏原件;用户偏好;偏好|02 经验与方法/Agent/用户偏好与工作约束.md|不要移动剪藏原件",
		"U01|镜像源;清华源|02 经验与方法/Agent/镜像.md|pip 走清华源",
		"C01|nginx;HTTP 200;前端服务端口冲突排查|02 经验与方法/从 Codex 导入/01_x.md|x",
	}, "\n"))

	p, err := 旧记忆库(v)
	if err != nil {
		t.Fatal(err)
	}
	byTo := map[string]Planned{}
	for _, n := range p.Notes {
		byTo[n.To] = n
	}
	if len(p.Notes) != 5 {
		t.Fatalf("want 3 rules + 2 notes, got %d: %v", len(p.Notes), keys(byTo))
	}
	clip, ok := byTo["rules/imported/不要移动剪藏原件.md"]
	if !ok {
		t.Fatalf("clippings rule missing: %v", keys(byTo))
	}
	if strings.Join(clip.Aliases, ",") != "剪藏,剪藏原件" {
		t.Fatalf("page-wide words must not become rule aliases: %v", clip.Aliases)
	}
	if _, ok := byTo["rules/imported/日常语言.md"]; !ok {
		t.Fatal("labelled bullet should keep its label as title")
	}
	codex := byTo["notes/imported/02 经验与方法/从 Codex 导入/01_x.md"]
	if strings.Join(codex.Aliases, ",") != "前端服务端口冲突排查" {
		t.Fatalf("machine-made short aliases should be dropped: %v", codex.Aliases)
	}
	mirror := byTo["notes/imported/02 经验与方法/Agent/镜像.md"]
	n := note.Parse(mirror.Content, mirror.To)
	if n.Title != "国内网络镜像" || n.Type != "howto" || !strings.HasPrefix(n.Body, "pip 走清华源") || len(n.Problems) > 0 {
		t.Fatalf("bad note: %+v", n)
	}
	reasons := map[string]bool{}
	for _, s := range p.Skipped {
		reasons[s.From] = true
	}
	for _, want := range []string{"Agent第二大脑.md", "02 经验与方法/准经验/准经验入口.md", "05 时间日志/2026-11/25｜某天.md"} {
		if !reasons[want] {
			t.Errorf("%s should be skipped", want)
		}
	}

	home := t.TempDir()
	w, kept, err := p.Write(home, false)
	if err != nil || w != 5 || kept != 0 {
		t.Fatalf("write: %d %d %v", w, kept, err)
	}
	w, kept, _ = p.Write(home, false)
	if w != 0 || kept != 5 {
		t.Fatalf("second import must not overwrite: %d %d", w, kept)
	}
}

func keys(m map[string]Planned) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
