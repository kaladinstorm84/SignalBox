//go:build windows

package cli

import (
	"os/exec"
	"testing"
)

func TestWorkerWindowsBackgroundFlags(t *testing.T) {
	cmd := exec.Command("signalbox", "_worker")
	detachWorker(cmd)
	const (
		noWindow   = 0x08000000
		breakaway  = 0x01000000
		detached   = 0x00000008
		newConsole = 0x00000010
	)
	flags := cmd.SysProcAttr.CreationFlags
	if flags&noWindow == 0 || flags&breakaway == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("worker must be windowless and break away from the server job: %#v", cmd.SysProcAttr)
	}
	if flags&(detached|newConsole) != 0 {
		t.Fatalf("worker flags conflict with CREATE_NO_WINDOW: %#x", flags)
	}
}
