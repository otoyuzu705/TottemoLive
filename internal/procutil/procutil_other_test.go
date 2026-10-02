//go:build !windows

package procutil

import (
	"os/exec"
	"testing"
)

// Windows以外では何も変えない。
func TestHideConsoleIsNoop(t *testing.T) {
	cmd := exec.Command("true")
	HideConsole(cmd)
	if cmd.SysProcAttr != nil {
		t.Errorf("unexpected SysProcAttr: %+v", cmd.SysProcAttr)
	}
}
