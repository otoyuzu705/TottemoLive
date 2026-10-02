//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
)

// createNoWindow は CREATE_NO_WINDOW(コンソールアプリにコンソール窓を作らない)。
const createNoWindow = 0x08000000

func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
