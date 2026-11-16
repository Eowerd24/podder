//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// The caller keeps the creating OS thread alive through Wait: Linux attaches
// PR_SET_PDEATHSIG to that thread, not just the Go process. The Go exec path
// also checks for parent death racing the prctl call before exec.
func configureTunnelLifetime(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return nil
}
