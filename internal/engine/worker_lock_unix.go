//go:build !windows

package engine

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockWorkerFile never waits: lock contention means another owner is alive.
// The OS releases this lock when its descriptor closes, including on process exit.
func lockWorkerFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
