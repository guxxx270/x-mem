package recall_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"x-mem/internal/config"
	"x-mem/internal/index"
	"x-mem/internal/inject"
	"x-mem/internal/recall"
)

func TestKnowledgeBaseIsNotMemory(t *testing.T) {
	home := t.TempDir()
	kb := t.TempDir()
	l := config.Layers{Home: home, Personal: filepath.Join(home, "personal"), State: filepath.Join(home, "state"),
		KB: []config.KB{{Path: kb, Exclude: []string{"私密"}}}}
	files := map[string]string{
		filepath.Join(l.Personal, "notes/db.md"):      "---\nid: db\ntitle: 订单服务数据库\ntype: decision\nrelations:\n  - op: \"@relates_to\"\n    target: backup\n---\n订单服务的数据库用 PostgreSQL 16。\n",
		filepath.Join(l.Personal, "notes/backup.md"):  "---\nid: backup\ntitle: 数据库备份\n---\n每天凌晨三点全量备份，保留 14 天。\n",
		filepath.Join(l.Personal, "notes/other.md"):   "---\nid: other\ntitle: 周报格式\n---\n周报三段。\n",
		filepath.Join(kb, "文章/向量数据库选型.md"):            "---\ntitle: 向量数据库选型\n---\nMilvus 和 Qdrant 的对比，Qdrant 部署更轻。\n",
		filepath.Join(kb, "私密/日记.md"):                 "向量数据库选型的私人想法。\n",
		filepath.Join(kb, "日志/2026-08-13｜Alembic.md"): "---\ntype: session-excerpt\ntitle: Alembic 必要性讨论\n---\n- 保留 Alembic，用它管理数据库迁移版本。\n",
	}
	for p, body := range files {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	ix, err := index.Open(l)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	res := recall.Search(ix, "Milvus 还是 Qdrant", recall.Options{})
	if res.Tier != recall.None {
		t.Fatalf("reference material must not make an answer from memory: %s", res.Tier)
	}
	for _, h := range res.Hits {
		if h.Doc.Layer == "kb" {
			t.Fatal("a knowledge-base page among memory hits")
		}
	}
	if len(res.KB) != 1 || res.KB[0].Doc.Title != "向量数据库选型" || strings.Contains(inject.Answer(recall.Search(ix, "向量数据库选型的私人想法", recall.Options{}), 3), "日记") {
		t.Fatalf("want the article as reference only (and not the excluded diary): %+v", res.KB)
	}
	out := inject.Answer(res, 3)
	if !strings.HasPrefix(out, "memory: none") || !strings.Contains(out, "不是用户定过的事") {
		t.Fatalf("answer must say this is reference, got:\n%s", out)
	}

	res = recall.Search(ix, "订单服务用什么数据库", recall.Options{})
	if res.Tier != recall.Direct || res.Hits[0].Doc.ID != "db" {
		t.Fatalf("memory first: %+v", res)
	}
	if len(res.Related) != 1 || res.Related[0].ID != "backup" {
		t.Fatalf("the related note should come along: %v", res.Related)
	}
	if out := inject.Answer(res, 3); !strings.Contains(out, "相关的笔记") || !strings.Contains(out, "数据库备份") {
		t.Fatalf("answer should list the related note:\n%s", out)
	}

	res = recall.Search(ix, "Alembic 要不要保留", recall.Options{})
	if out := inject.Answer(res, 3); !strings.Contains(out, "会话摘录") {
		t.Fatalf("a session excerpt should be labelled:\n%s", out)
	}
}
