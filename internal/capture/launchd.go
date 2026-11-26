package capture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// Label of the launchd job that runs the writer every half hour on macOS.
const Label = "local.x-mem.writer"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>writer</string><string>--once</string></array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>%s
    <key>PATH</key><string>%s</string>
  </dict>
</dict>
</plist>
`

// InstallTimer sets up the periodic scan. On macOS that is a launchd job;
// elsewhere it returns the crontab line to add by hand.
func InstallTimer(exe, stateDir string) (string, error) {
	if runtime.GOOS != "darwin" {
		return fmt.Sprintf("每 30 分钟扫一次：把这一行加进 crontab -e\n*/30 * * * * %s writer --once", exe), nil
	}
	env := ""
	if h := os.Getenv("X_MEM_HOME"); h != "" {
		env = "\n    <key>X_MEM_HOME</key><string>" + h + "</string>"
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		env += "\n    <key>CLAUDE_CONFIG_DIR</key><string>" + d + "</string>"
	}
	data := fmt.Sprintf(plist, Label, exe, int(ScanEvery.Seconds()), filepath.Join(stateDir, "writer.log"), env, os.Getenv("PATH"))
	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return "", err
	}
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+Label).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, path).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl bootstrap：%s", string(out))
	}
	return "定时扫描已装好（launchd：" + Label + "，每 30 分钟）", nil
}

// RemoveTimer undoes InstallTimer.
func RemoveTimer() string {
	if runtime.GOOS != "darwin" {
		return "如果加过 crontab 里的 x-mem writer --once 那一行，请手动删掉"
	}
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+Label).Run()
	if err := os.Remove(plistPath()); err == nil {
		return "定时扫描已移除"
	}
	return ""
}

// TimerInstalled reports whether the launchd job file exists.
func TimerInstalled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
