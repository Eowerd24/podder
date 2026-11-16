package main

import (
	"fmt"
	"strings"
)

func validateBindRestrictionSchema(spec ContainerSpec) error {
	for _, bind := range spec.Binds {
		if (bind.NoSuid || bind.NoDev) && spec.SchemaVersion < 4 {
			return fmt.Errorf("nosuid/nodev bind restrictions require spec schema 4 or later")
		}
	}
	return nil
}

// verifyRequestedBindRestrictions checks the engine's effective mount options,
// not merely the arguments sent to it. False retains legacy behavior: it does
// not request suid/dev or override additional restrictions imposed by the host.
func verifyRequestedBindRestrictions(raw inspectContainer, binds []BindMountSpec) error {
	for _, bind := range binds {
		if !bind.NoSuid && !bind.NoDev {
			continue
		}
		matches := 0
		for _, mount := range raw.Mounts {
			if mount.Destination != bind.ContainerPath {
				continue
			}
			matches++
			if mount.Type != "bind" {
				return fmt.Errorf("required bind restrictions could not be verified at %q", bind.ContainerPath)
			}
			options := map[string]bool{}
			for _, option := range mount.Options {
				options[strings.ToLower(strings.TrimSpace(option))] = true
			}
			if bind.ReadOnly && (mount.RW || options["rw"]) || bind.NoSuid && (!options["nosuid"] || options["suid"]) || bind.NoDev && (!options["nodev"] || options["dev"]) {
				return fmt.Errorf("required bind restrictions were not enforced at %q", bind.ContainerPath)
			}
		}
		if matches != 1 {
			return fmt.Errorf("required bind restrictions have no unique mount at %q", bind.ContainerPath)
		}
	}
	return nil
}

func (p *PodmanService) verifyContainerBindRestrictions(id string, binds []BindMountSpec) error {
	required := false
	for _, bind := range binds {
		required = required || bind.NoSuid || bind.NoDev
	}
	if !required {
		return nil
	}
	data, _, err := p.runCommand("inspect", "--format", "json", "--", id)
	if err != nil {
		return fmt.Errorf("required bind restrictions could not be inspected: %w", err)
	}
	raw, err := parseSingleInspect(data)
	if err != nil || raw.Id != id {
		return fmt.Errorf("required bind restrictions have no verified container identity")
	}
	return verifyRequestedBindRestrictions(raw, binds)
}
