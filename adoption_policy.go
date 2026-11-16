package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Podman 4.x exposes a string entrypoint and numeric stop signal; newer
// engines expose arrays and strings. Ambiguous legacy entrypoint strings
// are refused rather than guessed into a different argv.
func (c *inspectContainer) UnmarshalJSON(data []byte) error {
	type plain inspectContainer
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal(root["Config"], &cfg)
	if b := cfg["Entrypoint"]; len(b) > 0 && b[0] == '"' {
		var value string
		if err := json.Unmarshal(b, &value); err != nil {
			return err
		}
		args := []string{}
		if value != "" {
			if strings.ContainsAny(value, " \t\r\n") {
				return fmt.Errorf("ambiguous legacy entrypoint string; adoption refused")
			}
			args = []string{value}
		}
		cfg["Entrypoint"], _ = json.Marshal(args)
	}
	if b := cfg["StopSignal"]; len(b) > 0 && b[0] != '"' && string(b) != "null" {
		var n int
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		sig := strconv.Itoa(n)
		if n == 0 {
			sig = ""
		}
		if n == 15 {
			sig = "SIGTERM"
		}
		cfg["StopSignal"], _ = json.Marshal(sig)
	}
	root["Config"], _ = json.Marshal(cfg)
	normalized, _ := json.Marshal(root)
	var value plain
	if err := json.Unmarshal(normalized, &value); err != nil {
		return err
	}
	*c = inspectContainer(value)
	_ = json.Unmarshal(root["Config"], &c.configFields)
	_ = json.Unmarshal(root["HostConfig"], &c.hostFields)
	_ = json.Unmarshal(normalized, &c.rootFields)
	return nil
}
func emptyInspectValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
func oneOf(v any, values ...any) bool {
	for _, value := range values {
		if reflect.DeepEqual(v, value) {
			return true
		}
	}
	return false
}

// Podman's ExposedPorts is the union of image/--expose metadata and published
// container ports (libpod/container_inspect.go). Only entries already recreated
// by the exact published mappings are supported; expose-only ports need their
// own spec representation and must not be silently dropped.
func adoptionExposedPortsReplayed(raw inspectContainer, value any) bool {
	if value == nil {
		return true
	}
	ports, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, metadata := range ports {
		object, ok := metadata.(map[string]any)
		if !ok || len(object) != 0 {
			return false
		}
		port, protocol, ok := strings.Cut(key, "/")
		containerPort, err := strconv.ParseUint(port, 10, 16)
		if !ok || err != nil || containerPort == 0 || strconv.FormatUint(containerPort, 10) != port || (protocol != "tcp" && protocol != "udp") {
			return false
		}
		bindings := raw.HostConfig.PortBindings[key]
		if len(bindings) == 0 {
			return false
		}
		for _, binding := range bindings {
			hostPort, err := strconv.ParseUint(binding.HostPort, 10, 16)
			if err != nil || hostPort == 0 || strconv.FormatUint(hostPort, 10) != binding.HostPort {
				return false
			}
		}
	}
	return true
}

// Only named replayed fields and explicitly enumerated defaults are accepted.
// New engine fields block adoption until their semantics have been classified.
// Accepted default variants are compared again after replay, so a host default
// change cannot silently weaken an accepted original configuration.
func inspectPolicyBlockers(raw inspectContainer) []string {
	var out []string
	block := func(path string) {
		out = append(out, "unsupported inspect control "+path+"; adoption cannot reproduce or establish its default")
	}
	cfgReplayed := strings.Fields("Image Cmd Entrypoint Env Labels CreateCommand")
	cfgAssessed := strings.Fields("Hostname User WorkingDir StopSignal StopTimeout Healthcheck")
	hcAssessed := strings.Fields("PortBindings Binds Privileged NetworkMode RestartPolicy CapAdd CapDrop SecurityOpt PidMode IpcMode UsernsMode Dns DnsSearch ExtraHosts Devices AutoRemove Memory NanoCpus CpusetCpus Ulimits Init")
	contains := func(list []string, k string) bool {
		for _, s := range list {
			if s == k {
				return true
			}
		}
		return false
	}
	cfgZero := strings.Fields("Domainname AttachStdin AttachStdout AttachStderr Tty OpenStdin StdinOnce Volumes OnBuild Timeout Systemd ReadOnlyTmpfs Secrets HealthcheckStartup HealthcheckStartupCommand HealthcheckStartupInterval HealthcheckStartupTimeout HealthcheckStartupRetries HealthcheckStartupSuccesses")
	hcZero := strings.Fields("ContainerIDFile VolumeDriver VolumesFrom DnsOptions GroupAdd Cgroup Links OomScoreAdj ReadonlyRootfs Tmpfs Sysctls CpuShares BlkioWeight BlkioWeightDevice BlkioDeviceReadBps BlkioDeviceWriteBps BlkioDeviceReadIOps BlkioDeviceWriteIOps CpuPeriod CpuQuota CpuRealtimePeriod CpuRealtimeRuntime CpusetMems DiskQuota KernelMemory MemoryReservation MemorySwap MemorySwappiness OomKillDisable CpuCount CpuPercent IOMaximumIOps IOMaximumBandwidth CgroupConf DeviceCgroupRules PublishAllPorts Isolation ReadOnlyTmpfs")
	// Podman 5.4 and 6.2 report these health-log defaults even when no
	// healthcheck exists (libpod/define/healthchecks.go). They are not replayed
	// healthcheck controls: accept only the documented defaults with no check,
	// and retain their exact values in the post-replay comparison below.
	noHealthcheck := emptyInspectValue(raw.configFields["Healthcheck"]) && emptyInspectValue(raw.configFields["HealthcheckStartup"])
	annotations := func(v any) bool {
		if v == nil {
			return true
		}
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range m {
			switch k {
			case "io.container.manager":
				if v != "libpod" {
					return false
				}
			case "org.opencontainers.image.stopSignal", "org.systemd.property.KillSignal":
				if v != "15" {
					return false
				}
			case "org.systemd.property.TimeoutStopUSec":
				if v != "uint64 10000000" {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	for k, v := range raw.configFields {
		if contains(cfgReplayed, k) || contains(cfgAssessed, k) {
			continue
		}
		good := contains(cfgZero, k) && emptyInspectValue(v)
		switch k {
		case "Umask":
			good = oneOf(v, "", "0022")
		case "Passwd":
			good = v == true
		case "HealthcheckOnFailureAction":
			good = oneOf(v, "", "none")
		case "HealthLogDestination":
			good = noHealthcheck && oneOf(v, "", "local")
		case "HealthcheckMaxLogCount":
			good = noHealthcheck && v == float64(5)
		case "HealthcheckMaxLogSize":
			good = noHealthcheck && v == float64(500)
		case "ExposedPorts":
			good = adoptionExposedPortsReplayed(raw, v)
		case "sdNotifyMode":
			good = oneOf(v, "", "container")
		case "Annotations":
			good = annotations(v)
		}
		if !good {
			block("Config." + k)
		}
	}
	for k, v := range raw.hostFields {
		if contains(hcAssessed, k) {
			continue
		}
		good := contains(hcZero, k) && emptyInspectValue(v)
		switch k {
		case "PidsLimit":
			good = oneOf(v, float64(0), float64(2048))
		case "AutoRemoveImage":
			good = v == false
		case "HostsFile":
			good = v == ""
		case "ShmSize":
			good = oneOf(v, float64(0), float64(65536000))
		case "UTSMode", "CgroupnsMode", "CgroupMode":
			good = oneOf(v, nil, "", "private")
		case "CgroupManager":
			good = oneOf(v, "", "cgroupfs", "systemd")
		case "Cgroups":
			good = oneOf(v, "", "default")
		case "CgroupParent":
			good = oneOf(v, "", "user.slice")
		case "Runtime":
			good = oneOf(v, "", "oci")
		case "ConsoleSize":
			good = oneOf(v, nil, []any{float64(0), float64(0)})
		case "Annotations":
			good = annotations(v)
		case "LogConfig":
			m, ok := v.(map[string]any)
			good = ok
			for key, val := range m {
				switch key {
				case "Type":
					good = good && oneOf(val, "journald", "k8s-file", "json-file", "")
				case "Size":
					good = good && oneOf(val, "0B", "-1B", "")
				case "Path", "Tag":
					good = good && val == ""
				case "Config":
					good = good && emptyInspectValue(val)
				default:
					good = false
				}
			}
		}
		if !good {
			block("HostConfig." + k)
		}
	}
	knownRoot := strings.Fields("Id ID Created Path Args State Image ImageDigest ImageName Rootfs Pod OCIConfigPath ResolvConfPath HostnamePath HostsPath StaticDir OCIRuntime ConmonPidFile PidFile Name RestartCount Driver MountLabel ProcessLabel AppArmorProfile EffectiveCaps BoundingCaps ExecIDs GraphDriver Mounts NetworkSettings Namespace lockNumber Config HostConfig SizeRw SizeRootFs")
	for k, v := range raw.rootFields {
		if contains(knownRoot, k) {
			continue
		}
		if k == "KubeExitCodePropagation" && oneOf(v, nil, "", "invalid") {
			continue
		}
		if (k == "Dependencies" || k == "IsInfra" || k == "IsService") && emptyInspectValue(v) {
			continue
		}
		// These are real lifecycle and hosts-file controls, not arbitrary new
		// metadata. Only their explicit inactive values are representable.
		if (k == "UseImageHosts" || k == "UseImageHostname" || k == "IsInitCtr") && v == false {
			continue
		}
		if k == "InitContainerType" && v == "" {
			continue
		}
		block(k)
	}
	if command, ok := raw.configFields["CreateCommand"].([]any); ok {
		if err := validateAdoptionCreateCommand(command); err != nil {
			out = append(out, err.Error())
		}
	}
	sort.Strings(out)
	return out
}

func normalizedAdoptionInspect(raw inspectContainer) map[string]any {
	cfg := map[string]any{}
	hc := map[string]any{}
	for k, v := range raw.configFields {
		switch k {
		case "Hostname", "CreateCommand", "Image", "Labels":
			continue
		case "Env":
			if a, ok := v.([]any); ok {
				values := []string{}
				for _, x := range a {
					values = append(values, fmt.Sprint(x))
				}
				sort.Strings(values)
				v = values
			}
		}
		cfg[k] = v
	}
	labels := map[string]string{}
	for k, v := range raw.Config.Labels {
		if !isOwnershipLabel(k) {
			labels[k] = v
		}
	}
	cfg["Labels"] = labels
	// CreateCommand is engine-generated provenance, not a replayed control.
	for k, v := range raw.hostFields {
		if k == "Binds" {
			continue
		}
		hc[k] = v
	}
	return map[string]any{
		"Config": cfg, "HostConfig": hc, "Mounts": raw.Mounts,
		"AppArmorProfile":   raw.rootFields["AppArmorProfile"],
		"EffectiveCaps":     raw.rootFields["EffectiveCaps"],
		"BoundingCaps":      raw.rootFields["BoundingCaps"],
		"OCIRuntime":        raw.rootFields["OCIRuntime"],
		"UseImageHosts":     raw.rootFields["UseImageHosts"],
		"UseImageHostname":  raw.rootFields["UseImageHostname"],
		"IsInitCtr":         raw.rootFields["IsInitCtr"],
		"InitContainerType": raw.rootFields["InitContainerType"],
	}
}
func adoptionReplayEquivalent(before, after inspectContainer) bool {
	return reflect.DeepEqual(normalizedAdoptionInspect(before), normalizedAdoptionInspect(after))
}

func verifyAcceptedAdoptionInspect(accepted, current inspectContainer) error {
	if blockers := inspectPolicyBlockers(current); len(blockers) > 0 {
		return fmt.Errorf("inspect contains unclassified controls: %s", strings.Join(blockers, "; "))
	}
	if current.Id != accepted.Id || !adoptionReplayEquivalent(accepted, current) {
		return fmt.Errorf("inspect controls differ from the accepted original")
	}
	return nil
}
func parseSingleInspect(data string) (inspectContainer, error) {
	var list []inspectContainer
	err := json.Unmarshal([]byte(data), &list)
	if err != nil {
		return inspectContainer{}, err
	}
	if len(list) != 1 {
		return inspectContainer{}, fmt.Errorf("expected one exact inspect result")
	}
	return list[0], nil
}

func adoptionUlimit(name string, soft, hard int64) (UlimitSpec, error) {
	normalized := strings.ToLower(strings.TrimPrefix(strings.ToUpper(name), "RLIMIT_"))
	if (normalized != "nofile" && normalized != "nproc") || soft < 0 || hard < soft {
		return UlimitSpec{}, fmt.Errorf("ulimit %q cannot be faithfully replayed", name)
	}
	return UlimitSpec{normalized, soft, hard}, nil
}

func (p *PodmanService) verifyAcceptedAdoptionOriginal(id, accepted string) error {
	before, err := parseSingleInspect(accepted)
	if err != nil {
		return err
	}
	currentJSON, _, err := p.runCommand("inspect", "--format", "json", "--", id)
	if err != nil {
		return err
	}
	current, err := parseSingleInspect(currentJSON)
	if err != nil {
		return err
	}
	if current.Id != id {
		return fmt.Errorf("original inspect identity changed after assessment; adoption refused")
	}
	if err := verifyAcceptedAdoptionInspect(before, current); err != nil {
		return fmt.Errorf("original inspect controls changed after assessment; adoption refused")
	}
	return nil
}

// Older inspect schemas omit some controls (notably sysctls). Their original
// engine-generated argv must also contain only options represented by the spec.
func validateAdoptionCreateCommand(command []any) error {
	values := map[string]bool{"--root": true, "--runroot": true, "--storage-driver": true, "--events-backend": true, "--cgroup-manager": true, "--remote": true, "--name": true, "--label": true, "-l": true, "--publish": true, "-p": true, "--mount": true, "--volume": true, "-v": true, "--env-file": true, "--env": true, "-e": true, "--entrypoint": true, "--ulimit": true, "--pull": true}
	seenVerb := false
	for i := 1; i < len(command); i++ {
		arg, ok := command[i].(string)
		if !ok {
			return fmt.Errorf("invalid inspect CreateCommand")
		}
		if !seenVerb && (arg == "run" || arg == "create") {
			seenVerb = true
			continue
		}
		if arg == "--" {
			if !seenVerb {
				return fmt.Errorf("invalid create command boundary")
			}
			return nil
		}
		if arg == "-d" || arg == "--detach" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			key, _, hasValue := strings.Cut(arg, "=")
			if !values[key] {
				return fmt.Errorf("unsupported original create option %q; adoption refused", key)
			}
			if !hasValue {
				i++
				if i >= len(command) {
					return fmt.Errorf("missing create option value")
				}
			}
			continue
		}
		if seenVerb {
			return nil
		} // image and workload argv, never shell-interpreted
		return fmt.Errorf("unrecognized engine create command")
	}
	return fmt.Errorf("inspect create command has no image")
}
