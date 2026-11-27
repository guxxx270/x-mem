package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"x-mem/internal/mcp"
)

// x-mem self-install: put this program at ~/.x-mem/bin/x-mem unless a newer
// one is already there. The plugin's launcher runs it once per version, so
// the command line, the half-hourly capture job and every app share one
// stable copy that survives plugin updates.
func cmdSelfInstall(args []string) error {
	quiet := len(args) > 0 && args[0] == "--quiet"
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dst := stableBinary()
	stamp := filepath.Join(filepath.Dir(dst), ".version")
	have := ""
	if data, err := os.ReadFile(stamp); err == nil {
		have = strings.TrimSpace(string(data))
	}
	if _, err := os.Stat(dst); err == nil && have != "" && !newerVersion(mcp.Version, have) {
		return nil // the same or newer is installed
	}
	if same, _ := filepath.EvalSymlinks(dst); same == exe {
		return os.WriteFile(stamp, []byte(mcp.Version+"\n"), 0o644)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := fmt.Sprintf("%s.%d.tmp", dst, os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.WriteFile(stamp, []byte(mcp.Version+"\n"), 0o644); err != nil {
		return err
	}
	if !quiet {
		fmt.Printf("已装到 %s（%s）\n", dst, mcp.Version)
	}
	return nil
}

// stableBinary is where x-mem keeps the program every app and job uses.
func stableBinary() string {
	h := os.Getenv("X_MEM_HOME")
	if h == "" {
		h = filepath.Join(home(), ".x-mem")
	}
	name := "x-mem"
	if strings.HasSuffix(strings.ToLower(os.Args[0]), ".exe") {
		name += ".exe"
	}
	return filepath.Join(h, "bin", name)
}

// newerVersion compares dotted version numbers: is a newer than b?
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
