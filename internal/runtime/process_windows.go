//go:build windows

package runtime

import (
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	// A console-less worker's children otherwise allocate their own consoles.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
}
