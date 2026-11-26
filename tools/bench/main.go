// Command bench measures how long the per-message hook takes, as Claude Code
// runs it: a fresh process per message, against a vault of N generated notes.
//
//	go build -o bin/x-mem ./cmd/x-mem && go run ./tools/bench -bin bin/x-mem -notes 1000
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

const pool = "订单服务数据库接口超时发版测试部署缓存过期消息队列日志错误码分支命名前端框架会议周报剪藏原件归档文件夹用户权限支付回调重试监控告警配置环境变量版本升级迁移脚本备份恢复性能压测线程连接池索引查询报表导出"

func main() {
	bin := flag.String("bin", "bin/x-mem", "x-mem binary")
	notes := flag.Int("notes", 1000, "notes to generate")
	runs := flag.Int("runs", 200, "hook invocations")
	flag.Parse()
	exe, _ := filepath.Abs(*bin)

	home, _ := os.MkdirTemp("", "x-mem-bench-")
	defer os.RemoveAll(home)
	rs := []rune(pool)
	rng := rand.New(rand.NewSource(1))
	word := func(n int) string {
		out := make([]rune, n)
		for i := range out {
			out[i] = rs[rng.Intn(len(rs))]
		}
		return string(out)
	}
	for i := 0; i < *notes; i++ {
		dir := filepath.Join(home, "personal", "notes", fmt.Sprintf("g%02d", i%20))
		os.MkdirAll(dir, 0o755)
		body := fmt.Sprintf("---\nid: n%04d\ntitle: %s\ntype: decision\naliases: [%s]\n---\n%s，%s。\n%s %s。\n",
			i, word(6), word(4), word(40), word(30), word(25), word(20))
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("n%04d.md", i)), []byte(body), 0o644)
	}
	prompts := []string{"订单服务用什么数据库", "楼里的咖啡机又坏了", "接口超时设多少秒", "帮我看下这个报错", "下午把剪藏原件挪到归档文件夹"}
	env := append(os.Environ(), "X_MEM_HOME="+home, "X_MEM_PROJECT="+filepath.Join(home, "none"))

	first := time.Now()
	runOnce(exe, env, prompts[0], "warm")
	cold := time.Since(first)

	var ds []time.Duration
	for i := 0; i < *runs; i++ {
		start := time.Now()
		runOnce(exe, env, prompts[i%len(prompts)], fmt.Sprintf("s%d", i))
		ds = append(ds, time.Since(start))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	pct := func(p float64) time.Duration { return ds[int(float64(len(ds)-1)*p)] }
	fmt.Printf("%d notes, %d runs\nfirst run (builds the index): %s\np50 %s  p95 %s  max %s\n",
		*notes, *runs, cold.Round(time.Millisecond), pct(0.5).Round(100*time.Microsecond),
		pct(0.95).Round(100*time.Microsecond), ds[len(ds)-1].Round(100*time.Microsecond))
}

func runOnce(exe string, env []string, prompt, session string) {
	in, _ := json.Marshal(map[string]string{"prompt": prompt, "session_id": session})
	cmd := exec.Command(exe, "hook", "prompt")
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(in)
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hook failed:", err)
		os.Exit(1)
	}
}
