//go:build windows

package engine

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockWorkerFile never waits: lock contention means another owner is alive.
// The OS releases this lock when its handle closes, including on process exit.
func lockWorkerFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
}
