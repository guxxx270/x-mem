// buildplugin refreshes plugin/ for a release: x-mem for each platform,
// compressed, in plugin/libexec; the version in every manifest; the usage
// skill from the MCP instructions. With -zip it also writes
// dist/x-mem-plugin-<version>.zip, a marketplace folder to hand to people
// without access to the repository.
//
//	go run ./tools/buildplugin [-zip]
package main

import (
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"x-mem/internal/mcp"
)

var targets = []struct{ os, arch string }{
	{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"},
}

func main() {
	doZip := flag.Bool("zip", false, "also write dist/x-mem-plugin-<version>.zip")
	flag.Parse()
	v := mcp.Version
	must(os.MkdirAll("plugin/libexec", 0o755))
	old, _ := filepath.Glob("plugin/libexec/x-mem-*")
	for _, f := range old {
		os.Remove(f)
	}
	for _, t := range targets {
		name := fmt.Sprintf("x-mem-%s-%s", t.os, t.arch)
		if t.os == "windows" {
			name += ".exe"
		}
		tmp := filepath.Join(os.TempDir(), name)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", tmp, "./cmd/x-mem")
		cmd.Env = append(os.Environ(), "GOOS="+t.os, "GOARCH="+t.arch, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		must(cmd.Run())
		must(gz(tmp, filepath.Join("plugin/libexec", name+".gz")))
		os.Remove(tmp)
		fmt.Println("built", name)
	}
	must(os.WriteFile("plugin/libexec/VERSION", []byte(v+"\n"), 0o644))
	for _, m := range []string{"plugin/.claude-plugin/plugin.json", "plugin/plugin.json", "plugin/.antigravity-plugin/plugin.json"} {
		setJSON(m, func(o map[string]any) { o["version"] = v })
	}
	setJSON(".claude-plugin/marketplace.json", func(o map[string]any) {
		for _, p := range o["plugins"].([]any) {
			p.(map[string]any)["version"] = v
		}
	})
	must(os.MkdirAll("plugin/skills/x-mem", 0o755))
	must(os.WriteFile("plugin/skills/x-mem/SKILL.md", []byte(skill()), 0o644))
	if *doZip {
		out := filepath.Join("dist", "x-mem-plugin-"+v+".zip")
		must(os.MkdirAll("dist", 0o755))
		must(zipMarketplace(out))
		fmt.Println("wrote", out)
	}
}

func skill() string {
	return `---
name: x-mem
description: 查和记用户的长期记忆（x-mem）。用户提到以前定过的事、偏好、约定、项目里的做法，或者说“记住”“以后都……”“改成……”“过一下收件箱”时使用；用户的技能清单里点到的技能也从这里取。
---

# x-mem：用户的记忆库

` + mcp.Instructions + `

## 技能

会话开始时如果列了“你的技能”，那是用户从自己的经验里确认过的做法。手上的任务正好是某个技能说的情形时，用 read 工具按它的 id 取全文，照着做，并说明用了哪个技能；不是那个情形就不要提。
`
}

func gz(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	w, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	if _, err := io.Copy(w, in); err != nil {
		return err
	}
	return w.Close()
}

func setJSON(path string, edit func(map[string]any)) {
	data, err := os.ReadFile(path)
	must(err)
	var o map[string]any
	must(json.Unmarshal(data, &o))
	edit(o)
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	must(enc.Encode(o))
	must(os.WriteFile(path, []byte(b.String()), 0o644))
}

// zipMarketplace packs the marketplace file and the plugin into one folder,
// x-mem-plugin/, keeping the launcher executable.
func zipMarketplace(out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	add := func(path, name string, mode fs.FileMode) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()}
		h.SetMode(mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := add(".claude-plugin/marketplace.json", "x-mem-plugin/.claude-plugin/marketplace.json", 0o644); err != nil {
		return err
	}
	err = filepath.WalkDir("plugin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		mode := fs.FileMode(0o644)
		if strings.HasPrefix(filepath.ToSlash(p), "plugin/bin/") {
			mode = 0o755
		}
		return add(p, "x-mem-plugin/"+filepath.ToSlash(p), mode)
	})
	if err != nil {
		return err
	}
	return z.Close()
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
