//go:build !linux

package main

import (
	"fmt"
	"os/exec"
)

func configureTunnelLifetime(*exec.Cmd) error {
	return fmt.Errorf("SSH forwarding lifetime enforcement is unavailable on this platform")
}
