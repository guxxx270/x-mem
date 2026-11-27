package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"x-mem/internal/config"
	"x-mem/internal/transcript"
)

// The host is the agent that judges which candidate sentences are worth
// keeping. x-mem never calls a model itself: it borrows an agent you are
// already signed in to, run headless with no tools.
//
//   - codex (default): codex exec, read-only sandbox, final message to a file
//   - antigravity: the Antigravity CLI (agy -p)
//   - claude: claude -p, only when you choose it
//   - any other command: prompt on stdin, answer on stdout
//
// config.json "host" is ["codex"], ["antigravity"], ["claude"], a full path
// to one of them (["/opt/homebrew/bin/codex"]), or a whole command line.

// Hosts are the presets, in the order x-mem tries them when none is set.
var Hosts = []string{"codex", "antigravity"}

// hostKind names the preset a host setting stands for, or "" for a custom
// command.
func hostKind(host []string) string {
	if len(host) != 1 {
		return ""
	}
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(host[0]), ".exe")) {
	case "codex":
		return "codex"
	case "antigravity", "agy":
		return "antigravity"
	case "claude":
		return "claude"
	}
	return ""
}

// HostBinary finds the program for a preset: codex, agy or claude.
func HostBinary(kind string) (string, bool) {
	name := map[string]string{"codex": "codex", "antigravity": "agy", "claude": "claude"}[kind]
	if name == "" {
		return "", false
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	home, _ := os.UserHomeDir()
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local/bin"),
		filepath.Join(home, ".npm-global/bin"), filepath.Join(home, ".bun/bin")}
	if kind == "codex" { // the command line that comes with the Codex app
		for _, app := range []string{"/Applications/Codex.app", filepath.Join(home, "Applications/Codex.app")} {
			dirs = append(dirs, filepath.Join(app, "Contents/Resources/codex-cli/bin"),
				filepath.Join(app, "Contents/Resources/codex-cli/codex-path"), filepath.Join(app, "Contents/Resources"))
		}
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// ResolveHost turns the setting into a runnable command: the preset's
// binary found on this machine, or the custom command as given. With no
// setting it takes the first preset that is installed (never Claude).
func ResolveHost(host []string) (kind string, bin string, args []string, ok bool) {
	if len(host) == 0 {
		for _, k := range Hosts {
			if b, found := HostBinary(k); found {
				return k, b, nil, true
			}
		}
		return "", "", nil, false
	}
	if k := hostKind(host); k != "" {
		if filepath.IsAbs(host[0]) {
			if _, err := os.Stat(host[0]); err == nil {
				return k, host[0], nil, true
			}
		}
		b, found := HostBinary(k)
		return k, b, nil, found
	}
	return "custom", host[0], host[1:], true
}

// hostMarker sits in every prompt x-mem sends, so the host's own session is
// never read back as something you said.
const hostMarker = transcript.HostMarker

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[=>]`)

// runHost runs one distillation prompt and returns the agent's answer.
func runHost(l config.Layers, host []string, prompt string) (string, error) {
	kind, bin, extra, ok := ResolveHost(host)
	if !ok {
		return "", errors.New("没找到可用的整理助手（codex 或 agy）")
	}
	work := filepath.Join(l.State, "host")
	os.MkdirAll(work, 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	run := func(name string, args []string, stdin string) (string, string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = work // its own session lands under x-mem's home, which capture skips
		cmd.Env = append(os.Environ(), "X_MEM_DISABLE=1", "NO_COLOR=1")
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), strings.TrimSpace(errb.String()), err
	}
	switch kind {
	case "codex":
		last := filepath.Join(work, fmt.Sprintf("codex-%d.txt", os.Getpid()))
		defer os.Remove(last)
		args := []string{"exec", "--skip-git-repo-check", "-s", "read-only", "--color", "never",
			"-c", "model_reasoning_effort=low", "-o", last, "-"}
		out, errs, err := run(bin, args, prompt)
		if data, rerr := os.ReadFile(last); rerr == nil && strings.TrimSpace(string(data)) != "" {
			return string(data), nil
		}
		if err != nil {
			return "", fmt.Errorf("codex exec: %v: %s", err, cut(errs, 300))
		}
		return out, nil
	case "antigravity":
		args := []string{"-p", prompt}
		out, errs, err := run(bin, args, "")
		if strings.TrimSpace(out) == "" && err == nil && runtime.GOOS == "darwin" {
			// agy -p prints nothing when its output is not a terminal
			// (antigravity-cli issue #76): give it one through script(1).
			out, errs, err = run("/usr/bin/script", append([]string{"-q", "/dev/null", bin}, args...), "")
		}
		out = ansi.ReplaceAllString(out, "")
		if err != nil {
			return "", fmt.Errorf("agy -p: %v: %s", err, cut(errs, 300))
		}
		if strings.TrimSpace(out) == "" {
			return "", errors.New("agy -p 没有输出")
		}
		return out, nil
	case "claude":
		out, errs, err := run(bin, []string{"-p"}, prompt)
		if err != nil {
			return "", fmt.Errorf("claude -p: %v: %s", err, cut(errs, 300))
		}
		return out, nil
	}
	out, errs, err := run(bin, extra, prompt)
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, cut(errs, 300))
	}
	return out, nil
}

func cut(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "……"
}

// HostName is how a host setting is shown to you.
func HostName(host []string) string {
	kind, bin, _, ok := ResolveHost(host)
	switch {
	case !ok && len(host) == 0:
		return "没有（找不到 codex 或 agy：候选原句直接进收件箱）"
	case !ok:
		return strings.Join(host, " ") + "（这台机器上找不到）"
	case kind == "codex":
		return "Codex（" + bin + " exec）"
	case kind == "antigravity":
		return "Antigravity（" + bin + " -p）"
	case kind == "claude":
		return "Claude Code（" + bin + " -p）"
	}
	return strings.Join(host, " ")
}
