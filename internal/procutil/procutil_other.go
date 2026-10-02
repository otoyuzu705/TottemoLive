//go:build !windows

package procutil

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}
