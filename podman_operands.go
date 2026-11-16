package main

import (
	"fmt"
	"regexp"
	"strings"
)

var fullContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validatePodmanOperand(value string) error {
	if strings.TrimSpace(value) == "" || strings.HasPrefix(strings.TrimSpace(value), "-") || strings.ContainsRune(value, 0) {
		return fmt.Errorf("Podman positional value must be nonempty and must not begin with '-'")
	}
	return nil
}
func validateFullContainerID(id string) error {
	if err := validatePodmanOperand(id); err != nil {
		return err
	}
	if !fullContainerIDPattern.MatchString(id) {
		return fmt.Errorf("exact full container ID required; names and ID prefixes are not accepted")
	}
	return nil
}
