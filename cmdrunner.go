package main

import (
	"bytes"
	"os/exec"
)

// CommandRunner abstracts execution of external host commands (podman,
// systemctl, compose providers) so transaction and validation logic can be
// exercised in unit tests without touching the real host. Production code
// uses execCommandRunner; tests inject a scripted fake implementation so
// every failure point of a transaction (rename fails, stop fails, candidate
// create fails, verification times out, rollback partially fails, ...) can
// be exercised deterministically and without a real Podman/systemd install.
type CommandRunner interface {
	// Run executes name with args and returns captured stdout/stderr and any
	// error from the process (a non-zero exit is reported via err, mirroring
	// exec.Cmd.Run).
	Run(name string, args ...string) (stdout string, stderr string, err error)
}

// execCommandRunner is the production CommandRunner backed by os/exec.
type execCommandRunner struct{}

func (execCommandRunner) Run(name string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// defaultCommandRunner is used whenever a PodmanService is constructed
// without an explicit runner, i.e. the normal zero-value &PodmanService{}
// used throughout the application in production.
var defaultCommandRunner CommandRunner = execCommandRunner{}
