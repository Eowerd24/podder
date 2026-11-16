package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// BuildRunArgsFromSpec is the single, authoritative translator from a
// ContainerSpec to `podman run` arguments. Every Podder-managed deployment
// and mutation (container creation, DeploySpec, the port mutation
// transaction's candidate, and adoption's candidate) MUST go through this
// function so that one specification always produces one canonical runtime
// configuration — no separate partial argument builders for normal run,
// deployment from spec, port mutation, and adoption.
//
// It replays every field ContainerSpec represents: image, name, every
// published port, every bind mount, every environment variable, the
// command argv, entrypoint, and Podder labels. Nothing is silently dropped
// (no spec.Binds[0]-only, no ignored Env) and any error is returned instead
// of proceeding with a partial configuration.
func BuildRunArgsFromSpec(spec ContainerSpec) ([]string, error) {
	return buildRunArgsFromSpec(spec, true)
}

// Pure previews share translation and validation; execution additionally checks paths.
func buildRunArgsFromSpec(spec ContainerSpec, checkPaths bool) ([]string, error) {
	if checkPaths {
		env, err := specEnvironment(spec)
		if err != nil {
			return nil, err
		}
		spec.Env = env
	}
	if errs := ValidateSpec(spec); len(errs) > 0 {
		return nil, fmt.Errorf("invalid container spec: %s", strings.Join(errs, "; "))
	}

	image := strings.TrimSpace(spec.Image)
	if spec.Managed {
		image = strings.TrimSpace(spec.ResolvedImage)
	}
	args := []string{"run", "-d"}
	for _, limit := range spec.Ulimits {
		args = append(args, "--ulimit", fmt.Sprintf("%s=%d:%d", limit.Name, limit.Soft, limit.Hard))
	}

	name := strings.TrimSpace(spec.Name)
	if name != "" {
		args = append(args, "--name", name)
	}

	// Ordinary user labels are replayed first, in deterministic order.
	if len(spec.Labels) > 0 {
		keys := make([]string, 0, len(spec.Labels))
		for k := range spec.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--label", fmt.Sprintf("%s=%s", k, spec.Labels[k]))
		}
	}

	// Podder labels are applied if and only if the spec is explicitly
	// Managed — never inferred from the presence of port mappings or any
	// other field.
	if spec.Managed {
		args = append(args, "--label", "io.podder.managed=true")
		if name != "" {
			args = append(args, "--label", "io.podder.service="+name)
		}
		args = append(args, "--label", fmt.Sprintf("io.podder.schema-version=%d", spec.SchemaVersion))
	}

	// Every published port, not just the first.
	for _, m := range spec.PortMappings {
		if m.ContainerPort > 0 {
			args = append(args, "-p", FormatPublishSpec(m))
		}
	}

	// Every bind mount, not just the first.
	for _, b := range spec.Binds {
		hostPath := strings.TrimSpace(b.HostPath)
		containerPath := strings.TrimSpace(b.ContainerPath)
		if hostPath == "" || containerPath == "" {
			return nil, fmt.Errorf("bind mount is missing a host or container path")
		}
		if checkPaths {
			if _, err := os.Stat(hostPath); err != nil {
				return nil, fmt.Errorf("host path %q is not accessible: %w", hostPath, err)
			}
		}
		mountSpec := fmt.Sprintf("type=bind,src=%s,target=%s", hostPath, containerPath)
		if b.ReadOnly {
			mountSpec += ",readonly"
		}
		if b.NoSuid {
			mountSpec += ",nosuid"
		}
		if b.NoDev {
			mountSpec += ",nodev"
		}
		args = append(args, "--mount", mountSpec)
	}

	// A pure preview contains only a marker. Execution replaces it with a 0600
	// file in the private runtime directory, never values in process argv.
	if len(spec.Env) > 0 || spec.EnvironmentID != "" || len(spec.RequiredEnv) > 0 {
		args = append(args, "--env-file", privateEnvironmentPlaceholder)
	}

	if len(spec.Entrypoint) > 0 {
		entrypointArg, err := FormatEntrypointArg(spec.Entrypoint)
		if err != nil {
			return nil, err
		}
		args = append(args, "--entrypoint", entrypointArg)
	}

	args = append(args, "--", image)

	// Command argv is passed as discrete positional arguments — never
	// re-joined and re-split, so quoting/argument boundaries survive a
	// round trip exactly.
	if len(spec.Command) > 0 {
		args = append(args, []string(spec.Command)...)
	}

	return args, nil
}

// BuildCreateArgsFromSpec is BuildRunArgsFromSpec's non-starting
// counterpart: it produces `podman create` arguments for a spec so a
// replacement container can be recreated without being started, preserving
// an original workload's stopped lifecycle state (a mutation must never
// auto-restart a container that was deliberately stopped). It shares the
// exact same field translation as BuildRunArgsFromSpec — the run/create verb
// and the implicit start are the only difference.
func BuildCreateArgsFromSpec(spec ContainerSpec) ([]string, error) {
	runArgs, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		return nil, err
	}
	// runArgs is always ["run", "-d", ...]; podman create accepts the same
	// flags minus -d, which it does not recognize.
	createArgs := append([]string{"create"}, runArgs[2:]...)
	return createArgs, nil
}
