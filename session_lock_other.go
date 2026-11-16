//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Unsupported platforms refuse session mutation until an equivalent lock and
// stable directory identity implementation is available and verified.
func sessionDirectoryIdentity(string) (string, error) {
	return "", fmt.Errorf("session store identity unavailable on this platform")
}
func lockSessionFile(*os.File) error {
	return fmt.Errorf("session operation locking unavailable on this platform")
}
func inheritSessionLock(context.Context, *exec.Cmd) {}
