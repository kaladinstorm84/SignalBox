//go:build !windows

package runtime

import "os/exec"

// configureProcess is a no-op on Unix; console suppression is Windows-specific.
func configureProcess(cmd *exec.Cmd) {}
