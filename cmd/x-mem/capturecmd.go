package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"x-mem/internal/capture"
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
		fs.Parse(args[1:])
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
		fmt.Println("还需要 Stop 钩子：运行过 x-mem install claude-code --apply 就有了。")
	case "off":
		st.Enabled = false
		if err := capture.SaveState(l, st); err != nil {
			return err
		}
		fmt.Println("会话采集已关闭。")
		if msg := capture.RemoveTimer(); msg != "" {
			fmt.Println(msg)
		}
	case "status":
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
		return errors.New("用法：x-mem capture on [--exclude 目录] | off | status")
	}
	return nil
}
