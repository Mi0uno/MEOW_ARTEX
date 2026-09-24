//go:build !windows

package server

import (
	"os/exec"
)

// hideSubprocessWindow 非 Windows 平台无控制台窗口问题,空实现。
func hideSubprocessWindow(_ *exec.Cmd) {}
