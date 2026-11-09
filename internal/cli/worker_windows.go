//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

func detachWorker(cmd *exec.Cmd) {
	// BREAKAWAY is essential when serve itself belongs to a kill-on-close
	// job (terminals, service hosts, CI). Fail launch if the host forbids it.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000 | 0x00000200 | 0x01000000,
		HideWindow:    true,
	}
}
