//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestHideConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo", "ok")
	HideConsole(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("flags not set: %+v", cmd.SysProcAttr)
	}
	if out, err := cmd.Output(); err != nil || len(out) == 0 {
		t.Errorf("command should still run: %v %q", err, out)
	}
}

// すでに設定されたフラグ・属性は消さない。
func TestHideConsoleKeepsExistingFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo", "ok")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	HideConsole(cmd)
	if cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("flags %x", cmd.SysProcAttr.CreationFlags)
	}
}
