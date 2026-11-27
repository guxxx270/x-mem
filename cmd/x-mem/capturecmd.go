package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"x-mem/internal/capture"
	"x-mem/internal/config"
	"x-mem/internal/writer"
)

// x-mem capture on|off|status
func cmdCapture(args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	l, err := layers()
	if err != nil {
		return err
	}
	st := capture.LoadState(l)
	switch args[0] {
	case "on":
		fs := flag.NewFlagSet("capture on", flag.ExitOnError)
		var exclude multi
		fs.Var(&exclude, "exclude", "不采集这个项目目录，可写多次")
		host := fs.String("host", "", "用谁来整理候选：codex（默认）、antigravity，或 claude")
		fs.Parse(reorder(fs, args[1:]))
		if err := setHost(l, *host); err != nil {
			return err
		}
		for _, e := range exclude {
			abs, _ := filepath.Abs(e)
			st.Exclude = append(st.Exclude, abs)
		}
		if !st.Enabled {
			st.Since = time.Now()
		}
		st.Enabled = true
		if err := capture.SaveState(l, st); err != nil {
			return err
		}
		fmt.Println("会话采集已打开：只读打开之后的新内容，只取你说的话；候选进 inbox，等你确认才写入。")
		fmt.Println("不想被记的话放在 <private>…</private> 里；整个会话不想被记，就在消息里写 #不记。")
		if exe, err := os.Executable(); err == nil {
			exe, _ = filepath.EvalSymlinks(exe)
			msg, err := capture.InstallTimer(exe, l.State)
			if err != nil {
				fmt.Println("定时扫描没装上：", err)
			} else {
				fmt.Println(msg)
			}
		}
		fmt.Println("整理候选用：", capture.HostName(writer.LoadSettings(l.Home).Host))
		fmt.Println("还需要 Stop 钩子：运行过 x-mem install all --apply 就有了。")
	case "off":
		st.Enabled = false
		if err := capture.SaveState(l, st); err != nil {
			return err
		}
		fmt.Println("会话采集已关闭。")
		if msg := capture.RemoveTimer(); msg != "" {
			fmt.Println(msg)
		}
	case "host":
		if len(args) != 2 {
			return errors.New("用法：x-mem capture host codex|antigravity|claude")
		}
		if err := setHost(l, args[1]); err != nil {
			return err
		}
		fmt.Println("整理候选改用：", capture.HostName(writer.LoadSettings(l.Home).Host))
	case "status":
		fmt.Println("整理候选用：", capture.HostName(writer.LoadSettings(l.Home).Host))
		if st.Enabled {
			fmt.Printf("会话采集：开（%s 起）\n", st.Since.Format("2006-01-02 15:04"))
			if !st.LastScan.IsZero() {
				fmt.Printf("上次全量扫描：%s\n", st.LastScan.Format("2006-01-02 15:04"))
			}
			for _, e := range st.Exclude {
				fmt.Println("不采集：", e)
			}
		} else {
			fmt.Println("会话采集：关（x-mem capture on 打开）")
		}
	default:
		return errors.New("用法：x-mem capture on [--host codex|antigravity|claude] [--exclude 目录] | host <名字> | off | status")
	}
	return nil
}

// setHost records which agent sorts the candidates, with the full path of
// its program so the half-hourly job finds it too. Empty keeps the current
// choice, or picks Codex, then Antigravity, when there is none.
func setHost(l config.Layers, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		if cur := writer.LoadSettings(l.Home).Host; len(cur) > 0 {
			return nil
		}
		for _, k := range capture.Hosts {
			if bin, ok := capture.HostBinary(k); ok {
				return config.SetConfig(l.Home, "host", []string{bin})
			}
		}
		return nil // none installed: candidates go to the inbox as they are
	}
	kind := map[string]string{"codex": "codex", "antigravity": "antigravity", "agy": "antigravity", "claude": "claude"}[name]
	if kind == "" {
		return fmt.Errorf("不认识的整理助手 %q：可以用 codex、antigravity 或 claude", name)
	}
	bin, ok := capture.HostBinary(kind)
	if !ok {
		hint := map[string]string{
			"codex":       "没找到 codex 命令。Codex App 里的命令行要单独装：npm i -g @openai/codex，或 brew install codex",
			"antigravity": "没找到 agy（Antigravity 命令行）。它和 Antigravity App 是分开装的，装好并在终端里登录一次 agy 后再试",
			"claude":      "没找到 claude 命令",
		}[kind]
		return errors.New(hint)
	}
	return config.SetConfig(l.Home, "host", []string{bin})
}
