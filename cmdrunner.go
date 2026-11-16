package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
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

type CommandRunnerWithFiles interface {
	RunWithFiles(string, []*os.File, ...string) (string, string, error)
}

// execCommandRunner is the production CommandRunner backed by os/exec.
type execCommandRunner struct{}

// podmanInnerPrefix marks the real runner as capable of the Linux
// `podman unshare` mount-namespace handoff used for pinned bind mounts.
// Ordinary production stores need no extra arguments for the inner Podman.
func (execCommandRunner) podmanInnerPrefix() []string { return nil }

func (execCommandRunner) Run(name string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (execCommandRunner) RunWithFiles(name string, files []*os.File, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	cmd.ExtraFiles = append([]*os.File{}, files...)
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

// ContextCommandRunner is optional so existing transaction mocks stay compatible.
// Session builds require cancellation and bounded output without a shell.
type ContextCommandRunner interface {
	RunContext(context.Context, string, ...string) (string, string, error)
}
type ContextCommandRunnerWithFiles interface {
	RunContextWithFiles(context.Context, string, []*os.File, ...string) (string, string, error)
}
type boundedCommandOutput struct {
	data  []byte
	limit int
}

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (execCommandRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	return (execCommandRunner{}).RunContextWithFiles(ctx, name, nil, args...)
}
func (execCommandRunner) RunContextWithFiles(ctx context.Context, name string, files []*os.File, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.ExtraFiles = append([]*os.File{}, files...)
	inheritSessionLock(ctx, cmd)
	cmd.WaitDelay = 2 * time.Second
	if name == "git" {
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_LFS_SKIP_SMUDGE=1")
	}
	out := &boundedCommandOutput{limit: 128 * 1024}
	errs := &boundedCommandOutput{limit: 128 * 1024}
	cmd.Stdout = out
	cmd.Stderr = errs
	err := cmd.Run()
	return string(out.data), string(errs.data), err
}
func (p *PodmanService) runSessionCommand(ctx context.Context, name string, args ...string) (string, string, error) {
	runner := p.cmdRunner()
	if contextual, ok := runner.(ContextCommandRunner); ok {
		return contextual.RunContext(ctx, name, args...)
	}
	// Scripted unit-test runners are synchronous and perform no external work.
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	return runner.Run(name, args...)
}

func (p *PodmanService) runSessionCommandWithFiles(ctx context.Context, name string, files []*os.File, args ...string) (string, string, error) {
	runner := p.cmdRunner()
	if contextual, ok := runner.(ContextCommandRunnerWithFiles); ok {
		return contextual.RunContextWithFiles(ctx, name, files, args...)
	}
	if withFiles, ok := runner.(CommandRunnerWithFiles); ok && len(files) != 0 {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return withFiles.RunWithFiles(name, files, args...)
	}
	if len(files) != 0 {
		return "", "", fmt.Errorf("command runner cannot inherit pinned filesystem descriptors")
	}
	return p.runSessionCommand(ctx, name, args...)
}
