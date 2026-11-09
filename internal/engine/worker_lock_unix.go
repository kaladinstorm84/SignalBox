//go:build !windows

package engine

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockWorkerFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
