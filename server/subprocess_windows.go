//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// hideSubprocessWindow 在 Windows 上隐藏子进程的控制台窗口。
// 没有这个调用,每个 exec.Command 生成的子进程都会弹出一个可见的 cmd/powershell
// 窗口,在任务执行期间反复闪烁。
func hideSubprocessWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
