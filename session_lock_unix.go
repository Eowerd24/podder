//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func sessionDirectoryIdentity(path string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", fmt.Errorf("not a directory")
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}
func lockSessionFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
func inheritSessionLock(ctx context.Context, cmd *exec.Cmd) {
	if file, ok := ctx.Value(sessionLockContextKey{}).(*os.File); ok {
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	}
}
