package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"x-mem/internal/textutil"
)

func TestStartCatalogWithinBudget(t *testing.T) {
	home, proj := setup(t)
	for i := 0; i < 400; i++ {
		p := filepath.Join(home, "personal/notes", fmt.Sprintf("n%03d.md", i))
		os.WriteFile(p, []byte(fmt.Sprintf("---\ntitle: 第%d号机房的网络配置说明\naliases: [机房%d]\n---\n内容。\n", i, i)), 0o644)
	}
	ctx := context(t, runHook(t, "start", "cat", "", proj))
	if !strings.Contains(ctx, "记忆目录") || !strings.Contains(ctx, "推送前跑全量测试") {
		t.Fatalf("start block should hold the rules and a catalog:\n%s", ctx)
	}
	if n := textutil.EstimateTokens(ctx); n > StartBudget+60 {
		t.Fatalf("start block is %d tokens, budget %d", n, StartBudget)
	}
	if !strings.Contains(ctx, "还有") {
		t.Fatal("a catalog cut by the budget should say how many are left")
	}
}
