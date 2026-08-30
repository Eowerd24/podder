package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AdoptionAssessment models the safety analysis and proposed spec for
// adopting a container. CanAdopt is default-deny: any inspected feature
// Podder's current spec schema cannot faithfully reproduce is a Blocker,
// not a Warning, and adoption refuses rather than silently dropping it.
type AdoptionAssessment struct {
	ContainerID   string        `json:"containerId"`
	ContainerName string        `json:"containerName"`
	CanAdopt      bool          `json:"canAdopt"`
	Blockers      []string      `json:"blockers,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	ProposedSpec  ContainerSpec `json:"proposedSpec"`
	RawInspect    string        `json:"rawInspect,omitempty"`
}

// AdoptionResult represents the outcome of an adoption transaction. Success
// is only ever true after a complete supported spec was built, the
// container was safely recreated, its runtime configuration and Podder
// labels were verified, and the spec commit itself succeeded.
type AdoptionResult struct {
	Success                bool            `json:"success"`
	ServiceName            string          `json:"serviceName"`
	Spec                   ContainerSpec   `json:"spec"`
	Message                string          `json:"message"`
	Rollback               *RollbackResult `json:"rollback,omitempty"`
	ManualRecoveryRequired bool            `json:"manualRecoveryRequired,omitempty"`
	BackupCleanupRequired  bool            `json:"backupCleanupRequired,omitempty"`
	BackupContainerName    string          `json:"backupContainerName,omitempty"`
}

// Raw inspect structures. These deliberately capture more than the fields
// Podder can currently represent, so assessRepresentability can detect
// unsupported non-default configuration instead of silently ignoring it.
type inspectContainer struct {
	Id     string   `json:"Id"`
	Name   string   `json:"Name"`
	Image  string   `json:"Image"`
	Path   string   `json:"Path"`
	Args   []string `json:"Args"`
	Pod    string   `json:"Pod"`
	Config struct {
		Image       string            `json:"Image"`
		Cmd         []string          `json:"Cmd"`
		Entrypoint  []string          `json:"Entrypoint"`
		Env         []string          `json:"Env"`
		Labels      map[string]string `json:"Labels"`
		WorkingDir  string            `json:"WorkingDir"`
		User        string            `json:"User"`
		Hostname    string            `json:"Hostname"`
		StopSignal  string            `json:"StopSignal"`
		StopTimeout *int              `json:"StopTimeout"`
		Healthcheck *struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		Binds         []string `json:"Binds"`
		Privileged    bool     `json:"Privileged"`
		NetworkMode   string   `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		CapAdd      []string `json:"CapAdd"`
		CapDrop     []string `json:"CapDrop"`
		SecurityOpt []string `json:"SecurityOpt"`
		PidMode     string   `json:"PidMode"`
		IpcMode     string   `json:"IpcMode"`
		UsernsMode  string   `json:"UsernsMode"`
		Dns         []string `json:"Dns"`
		DnsSearch   []string `json:"DnsSearch"`
		ExtraHosts  []string `json:"ExtraHosts"`
		Devices     []struct {
			PathOnHost string `json:"PathOnHost"`
		} `json:"Devices"`
		Memory     int64  `json:"Memory"`
		NanoCpus   int64  `json:"NanoCpus"`
		CpusetCpus string `json:"CpusetCpus"`
		Ulimits    []struct {
			Name string `json:"Name"`
		} `json:"Ulimits"`
		Init *bool `json:"Init"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string   `json:"Type"`
		Name        string   `json:"Name"`
		Source      string   `json:"Source"`
		Destination string   `json:"Destination"`
		RW          bool     `json:"RW"`
		Driver      string   `json:"Driver"`
		Mode        string   `json:"Mode"`
		Options     []string `json:"Options"`
		Propagation string   `json:"Propagation"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			Aliases    []string `json:"Aliases"`
			IPAMConfig *struct {
				IPv4Address string `json:"IPv4Address"`
				IPv6Address string `json:"IPv6Address"`
			} `json:"IPAMConfig"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// assessRepresentability inspects every field Podder does not yet model and
// reports each as a blocker. This is a default-deny gate: an unrecognized
// or non-default field is treated as unsafe to ignore, never as harmless.
func assessRepresentability(raw inspectContainer) []string {
	var blockers []string
	add := func(format string, args ...interface{}) {
		blockers = append(blockers, fmt.Sprintf(format, args...))
	}

	if raw.HostConfig.Privileged {
		add("container runs --privileged, which Podder does not yet reproduce")
	}

	netMode := strings.ToLower(strings.TrimSpace(raw.HostConfig.NetworkMode))
	if netMode == "host" {
		add("container uses host networking, which Podder does not yet reproduce")
	}

	restartName := strings.ToLower(strings.TrimSpace(raw.HostConfig.RestartPolicy.Name))
	if restartName != "" && restartName != "no" {
		add("container has a custom restart policy (%q), which Podder does not yet reproduce", raw.HostConfig.RestartPolicy.Name)
	}

	if len(raw.HostConfig.CapAdd) > 0 || len(raw.HostConfig.CapDrop) > 0 {
		add("container adds or drops Linux capabilities, which Podder does not yet reproduce")
	}

	for _, opt := range raw.HostConfig.SecurityOpt {
		if strings.TrimSpace(opt) != "" {
			add("container sets a security option (%q, including SELinux options), which Podder does not yet reproduce", opt)
			break
		}
	}

	if pid := strings.TrimSpace(raw.HostConfig.PidMode); pid != "" {
		add("container uses a non-default PID namespace (%q), which Podder does not yet reproduce", pid)
	}

	if ipc := strings.TrimSpace(raw.HostConfig.IpcMode); ipc != "" && ipc != "shareable" && ipc != "private" {
		add("container uses a non-default IPC namespace (%q), which Podder does not yet reproduce", ipc)
	}

	if userns := strings.TrimSpace(raw.HostConfig.UsernsMode); userns != "" {
		add("container uses a non-default user namespace (%q), which Podder does not yet reproduce", userns)
	}

	if len(raw.HostConfig.Dns) > 0 || len(raw.HostConfig.DnsSearch) > 0 {
		add("container has custom DNS configuration, which Podder does not yet reproduce")
	}

	if len(raw.HostConfig.ExtraHosts) > 0 {
		add("container has extra /etc/hosts entries, which Podder does not yet reproduce")
	}

	if len(raw.HostConfig.Devices) > 0 {
		add("container has device mappings (including GPU passthrough), which Podder does not yet reproduce")
	}

	if raw.HostConfig.Memory > 0 {
		add("container has a memory limit, which Podder does not yet reproduce")
	}

	if raw.HostConfig.NanoCpus > 0 || strings.TrimSpace(raw.HostConfig.CpusetCpus) != "" {
		add("container has CPU limits or a cpuset, which Podder does not yet reproduce")
	}

	if len(raw.HostConfig.Ulimits) > 0 {
		add("container has custom ulimits, which Podder does not yet reproduce")
	}

	if raw.HostConfig.Init != nil && *raw.HostConfig.Init {
		add("container uses --init, which Podder does not yet reproduce")
	}

	if raw.Config.Healthcheck != nil && len(raw.Config.Healthcheck.Test) > 0 {
		test := strings.Join(raw.Config.Healthcheck.Test, ",")
		if !strings.EqualFold(test, "NONE") {
			add("container defines a Podman healthcheck, which Podder does not yet reproduce")
		}
	}

	if strings.TrimSpace(raw.Config.User) != "" {
		add("container runs as a custom user (%q), which Podder does not yet reproduce", raw.Config.User)
	}

	if strings.TrimSpace(raw.Config.WorkingDir) != "" {
		add("container sets a custom working directory (%q), which Podder does not yet reproduce", raw.Config.WorkingDir)
	}

	if hn := strings.TrimSpace(raw.Config.Hostname); hn != "" && !strings.HasPrefix(strings.TrimSpace(raw.Id), hn) {
		add("container sets a custom hostname (%q), which Podder does not yet reproduce", hn)
	}

	if sig := strings.TrimSpace(raw.Config.StopSignal); sig != "" && !strings.EqualFold(sig, "SIGTERM") {
		add("container sets a custom stop signal (%q), which Podder does not yet reproduce", sig)
	}
	if raw.Config.StopTimeout != nil && *raw.Config.StopTimeout != 0 && *raw.Config.StopTimeout != 10 {
		add("container sets a custom stop timeout (%ds), which Podder does not yet reproduce", *raw.Config.StopTimeout)
	}

	for netName, net := range raw.NetworkSettings.Networks {
		if netName != "" && netName != "podman" {
			add("container is attached to network %q, which Podder does not yet reproduce (only the default network attachment is supported)", netName)
		}
		if len(net.Aliases) > 0 {
			add("container has custom network aliases, which Podder does not yet reproduce")
		}
		if net.IPAMConfig != nil && (net.IPAMConfig.IPv4Address != "" || net.IPAMConfig.IPv6Address != "") {
			add("container has a static container IP, which Podder does not yet reproduce")
		}
	}

	for _, m := range raw.Mounts {
		switch m.Type {
		case "volume":
			if strings.TrimSpace(m.Name) != "" {
				add("container uses named volume %q, which Podder does not yet reproduce by name", m.Name)
			} else {
				add("container uses an anonymous volume mounted at %q, which Podder cannot safely reproduce (its contents would not be preserved)", m.Destination)
			}
		case "tmpfs":
			add("container uses a tmpfs mount at %q, which Podder does not yet reproduce", m.Destination)
		case "bind":
			if strings.Contains(m.Source, "/run/secrets") || strings.Contains(m.Destination, "/run/secrets") {
				add("container mounts a secret-style path at %q, which Podder does not yet reproduce", m.Destination)
			}
			if strings.TrimSpace(m.Driver) != "" || strings.TrimSpace(m.Mode) != "" {
				add("bind mount at %q uses driver/mode semantics Podder cannot reproduce", m.Destination)
			}
			propagation := strings.ToLower(strings.TrimSpace(m.Propagation))
			if propagation != "" && propagation != "rprivate" && propagation != "private" {
				add("bind mount at %q uses non-default propagation %q, which Podder cannot reproduce", m.Destination, m.Propagation)
			}
			for _, option := range m.Options {
				switch strings.ToLower(strings.TrimSpace(option)) {
				case "", "rw", "ro", "rbind", "private", "rprivate":
				default:
					add("bind mount at %q uses non-default option %q, which Podder cannot reproduce", m.Destination, option)
				}
			}
		}
	}

	return blockers
}

// ParseInspectToAssessment converts podman inspect JSON into an AdoptionAssessment.
func ParseInspectToAssessment(inspectJSON []byte) (*AdoptionAssessment, error) {
	var list []inspectContainer
	if err := json.Unmarshal(inspectJSON, &list); err != nil {
		return nil, fmt.Errorf("failed to parse inspect JSON: %w", err)
	}

	if len(list) == 0 {
		return nil, fmt.Errorf("no container details found in inspect JSON")
	}

	raw := list[0]
	containerName := strings.TrimPrefix(raw.Name, "/")

	assessment := &AdoptionAssessment{
		ContainerID:   raw.Id,
		ContainerName: containerName,
		CanAdopt:      true,
		Blockers:      []string{},
		Warnings:      []string{},
	}

	// Provenance classification: an externally-owned workload is never
	// adopted out from under its real owner.
	prov := ClassifyProvenance(raw.Config.Labels, raw.Pod, "")
	if prov.Type == "compose" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container is managed by Docker/Podman Compose. Adopt via Compose directly.")
	} else if prov.Type == "quadlet" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container is managed by systemd Quadlet. Unit file is the source of truth.")
	} else if prov.Type == "pod" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, fmt.Sprintf("Container is a member of Pod '%s'. Adopt the Pod rather than an individual member.", prov.PodName))
	} else if prov.Type == "podder" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container already carries Podder ownership metadata. Adoption is blocked; repair or remove the inconsistent managed state manually before retrying.")
	} else if prov.Type == "ambiguous" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container has conflicting ownership evidence ("+prov.AmbiguityReason+"). Resolve the conflicting labels before adopting.")
	}

	// Default-deny representability gate: any inspected field Podder cannot
	// faithfully reproduce blocks adoption outright.
	for _, b := range assessRepresentability(raw) {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, b)
	}

	// 1. Port mappings
	var portMappings []PortMapping
	for portKey, bindings := range raw.HostConfig.PortBindings {
		parts := strings.Split(portKey, "/")
		cPort, _ := strconv.Atoi(parts[0])
		proto := "tcp"
		if len(parts) > 1 {
			proto = parts[1]
		}

		for _, b := range bindings {
			hPort, _ := strconv.Atoi(b.HostPort)
			hIP := b.HostIP
			if hIP == "" {
				hIP = "0.0.0.0"
			}
			portMappings = append(portMappings, PortMapping{
				HostIP:        hIP,
				HostPort:      uint16(hPort),
				ContainerPort: uint16(cPort),
				Protocol:      proto,
			})
		}
	}

	// 2. Bind mounts (named/anonymous volumes and tmpfs are handled as
	// blockers above, not silently folded in here).
	var binds []BindMountSpec
	for _, m := range raw.Mounts {
		if m.Type == "bind" {
			binds = append(binds, BindMountSpec{
				HostPath:      m.Source,
				ContainerPath: m.Destination,
				ReadOnly:      !m.RW,
			})
		}
	}

	// 3. Environment variables are preserved exactly. Guessing that PATH, HOME,
	// TERM, or HOSTNAME is disposable can change workload semantics.
	envMap := make(map[string]string)
	for _, e := range raw.Config.Env {
		kv := strings.SplitN(e, "=", 2)
		if len(kv) == 2 {
			k := kv[0]
			v := kv[1]
			envMap[k] = v
		}
	}

	// Preserve ordinary workload labels exactly. Ownership markers are never
	// copied: external ownership blocks adoption above, and Podder ownership
	// is generated only after the replacement transaction succeeds.
	labels := make(map[string]string)
	for key, value := range raw.Config.Labels {
		if isOwnershipLabel(key) {
			continue
		}
		labels[key] = value
	}

	imageName := raw.Config.Image
	if imageName == "" {
		imageName = raw.Image
	}

	assessment.ProposedSpec = ContainerSpec{
		SchemaVersion:  CurrentSpecSchemaVersion,
		Name:           containerName,
		Image:          imageName,
		ResolvedImage:  raw.Image,
		ReplayComplete: true,
		PortMappings:   portMappings,
		Binds:          binds,
		Env:            envMap,
		Labels:         labels,
		// Command/Entrypoint come directly from Podman's own argv arrays —
		// no shell string round trip, so no lossy re-tokenization.
		Command:    CommandArgv(raw.Config.Cmd),
		Entrypoint: raw.Config.Entrypoint,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	return assessment, nil
}

// InspectContainerForAdoption returns an analysis and proposed declarative spec for a container.
func (p *PodmanService) InspectContainerForAdoption(containerID string) (*AdoptionAssessment, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil, fmt.Errorf("container id cannot be empty")
	}

	stdout, stderr, err := p.runCommand("inspect", "--format", "json", containerID)
	if err != nil {
		return nil, fmt.Errorf("podman inspect failed: %v (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	assessment, err := ParseInspectToAssessment([]byte(stdout))
	if err != nil {
		return nil, err
	}

	return assessment, nil
}

func adoptionRollbackResult(rb *RollbackResult, reason string) *AdoptionResult {
	res := &AdoptionResult{Success: false, Rollback: rb}
	if rb != nil && rb.Verified {
		res.Message = fmt.Sprintf("%s The original container was restored to its prior name and lifecycle.", reason)
	} else {
		res.ManualRecoveryRequired = true
		errs := ""
		if rb != nil {
			errs = strings.Join(rb.Errors, "; ")
		}
		res.Message = fmt.Sprintf("%s ROLLBACK FAILED / MANUAL RECOVERY REQUIRED: %s", reason, errs)
	}
	return res
}

// AdoptContainer converts an ad-hoc (or otherwise unmanaged) container into
// a Podder-managed workload. It never marks ownership before conversion
// succeeds:
//
//  1. assess representability (default-deny; any unsupported feature blocks
//     adoption outright, and nothing is touched)
//  2. build and validate a candidate spec, and persist it only as a
//     candidate — not yet authoritative
//  3. quiesce and recreate the container from the exact candidate spec,
//     preserving its original lifecycle
//  4. verify the replacement exists, has the expected lifecycle, has the
//     exact candidate port mappings, and actually carries Podder's managed
//     labels
//  5. only then commit the candidate spec and remove the backup
//
// Every returned error or Success=false leaves the original workload
// untouched (or restored via a reported, truthfully-verified rollback) and
// commits no spec. The caller must inspect the returned result — this
// function never silently ignores its own outcome, and it never reports
// Success unconditionally.
func (p *PodmanService) AdoptContainer(containerID string, serviceName string) (*AdoptionResult, error) {
	assessment, err := p.InspectContainerForAdoption(containerID)
	if err != nil {
		return nil, fmt.Errorf("adoption preflight failed: %w", err)
	}

	if !assessment.CanAdopt {
		return &AdoptionResult{
			Success: false,
			Message: fmt.Sprintf("Adoption blocked: %s", strings.Join(assessment.Blockers, "; ")),
		}, nil
	}

	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		serviceName = assessment.ContainerName
	}
	if serviceName != assessment.ContainerName {
		return &AdoptionResult{Success: false, Message: "Adoption blocked: changing the runtime container name during adoption is not safely representable; use the existing container name."}, nil
	}

	candidateSpec := assessment.ProposedSpec
	candidateSpec.Name = serviceName
	candidateSpec.Managed = true

	if errs := ValidateSpec(candidateSpec); len(errs) > 0 {
		return &AdoptionResult{Success: false, Message: fmt.Sprintf("Adoption blocked: candidate spec failed validation: %s", strings.Join(errs, "; "))}, nil
	}
	if _, err := BuildRunArgsFromSpec(candidateSpec); err != nil {
		return &AdoptionResult{Success: false, Message: fmt.Sprintf("Adoption blocked: candidate spec is not replayable: %v", err)}, nil
	}

	target, err := p.findContainerForAdoption(containerID)
	if err != nil {
		return nil, err
	}
	originalLifecycle, lifecycleSupported := classifyLifecycle(target.State)
	if !lifecycleSupported {
		return &AdoptionResult{Success: false, Message: fmt.Sprintf("Adoption blocked: container lifecycle state %q cannot be safely reproduced.", target.State)}, nil
	}
	if len(target.Names) == 0 || strings.TrimPrefix(target.Names[0], "/") != assessment.ContainerName {
		return &AdoptionResult{Success: false, Message: "Adoption blocked: runtime identity changed after assessment."}, nil
	}
	if strings.TrimSpace(target.ImageID) == "" || strings.TrimSpace(target.ImageID) != strings.TrimSpace(candidateSpec.ResolvedImage) {
		return &AdoptionResult{Success: false, Message: "Adoption blocked: runtime image identity changed or could not be verified after assessment."}, nil
	}
	if ok, _, _ := portMappingSetEqual(candidateSpec.PortMappings, target.PortMappings); !ok {
		return &AdoptionResult{Success: false, Message: "Adoption blocked: runtime port configuration changed after assessment."}, nil
	}
	if _, err := p.validateMappingsForMutation(candidateSpec.PortMappings, target.Id); err != nil {
		return &AdoptionResult{Success: false, Message: "Adoption blocked by final backend port validation: " + err.Error()}, nil
	}

	// Persist a candidate/draft spec only — ownership is never marked
	// before the workload has successfully become reproducible and
	// verified.
	candidatePath, err := writeCandidateSpec(candidateSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to persist candidate spec: %w", err)
	}

	backupName := newBackupName(assessment.ContainerName)

	if _, _, err := p.runCommand("rename", assessment.ContainerName, backupName); err != nil {
		discardCandidateSpec(candidatePath)
		return nil, fmt.Errorf("adoption failed: could not rename original container: %w", err)
	}

	if originalLifecycle == lifecycleRunning {
		if err := p.StopContainer(backupName); err != nil {
			discardCandidateSpec(candidatePath)
			rb := p.executeRollback(backupName, assessment.ContainerName, serviceName, target.Id, originalLifecycle, false)
			return adoptionRollbackResult(rb, fmt.Sprintf("Adoption failed: could not stop original container: %v.", err)), nil
		}
		stopped := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
			containers, listErr := p.ListContainers(true)
			if listErr != nil {
				return false
			}
			c := findContainerByName(containers, backupName)
			if c == nil {
				return false
			}
			kind, _ := classifyLifecycle(c.State)
			return kind == lifecycleStopped
		})
		if !stopped {
			discardCandidateSpec(candidatePath)
			rb := p.executeRollback(backupName, assessment.ContainerName, serviceName, target.Id, originalLifecycle, false)
			return adoptionRollbackResult(rb, "Adoption failed: original container did not verify stopped."), nil
		}
	}

	var createArgs []string
	if originalLifecycle == lifecycleRunning {
		createArgs, err = BuildRunArgsFromSpec(candidateSpec)
	} else {
		createArgs, err = BuildCreateArgsFromSpec(candidateSpec)
	}
	if err != nil {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, serviceName, target.Id, originalLifecycle, false)
		return adoptionRollbackResult(rb, fmt.Sprintf("Adoption failed: %v.", err)), nil
	}

	stdout, stderr, err := p.runCommand(createArgs...)
	if err != nil {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, serviceName, target.Id, originalLifecycle, false)
		return adoptionRollbackResult(rb, fmt.Sprintf("Adoption failed to recreate container: %v (stderr: %s).", err, strings.TrimSpace(stderr))), nil
	}
	newContainerID := strings.TrimSpace(stdout)
	if newContainerID == "" {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, serviceName, target.Id, originalLifecycle, false)
		return adoptionRollbackResult(rb, "Adoption failed: replacement creation returned no container identity; ambiguous candidate was not deleted by name."), nil
	}

	var newContainer *Container
	verified := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
		cs, err := p.ListContainers(true)
		if err != nil {
			return false
		}
		c := findContainerByIdentity(cs, newContainerID)
		if c == nil {
			c = findContainerByName(cs, serviceName)
		}
		if c == nil {
			return false
		}
		kind, _ := classifyLifecycle(c.State)
		if kind != originalLifecycle {
			return false
		}
		newContainer = c
		return true
	})
	if !verified || newContainer == nil {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, newContainerID, target.Id, originalLifecycle, true)
		return adoptionRollbackResult(rb, "Adoption failed: replacement container did not verify."), nil
	}

	if eq, missing, unexpected := portMappingSetEqual(candidateSpec.PortMappings, newContainer.PortMappings); !eq {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, newContainerID, target.Id, originalLifecycle, true)
		return adoptionRollbackResult(rb, fmt.Sprintf("Adoption failed: port mappings do not match after recreation (missing: %v, unexpected: %v).", missing, unexpected)), nil
	}

	if newContainer.Provenance.Type != "podder" || !containerMatchesSpecLabels(newContainer, candidateSpec) {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, assessment.ContainerName, newContainerID, target.Id, originalLifecycle, true)
		return adoptionRollbackResult(rb, "Adoption failed: replacement container did not verify as Podder-managed."), nil
	}

	if err := p.commitCandidate(candidatePath, candidateSpec); err != nil {
		rb := p.executeRollback(backupName, assessment.ContainerName, newContainerID, target.Id, originalLifecycle, true)
		return adoptionRollbackResult(rb, fmt.Sprintf("Adoption failed: could not commit spec: %v.", err)), nil
	}

	if err := p.RemoveContainer(backupName); err != nil {
		return &AdoptionResult{
			Success:               true,
			ServiceName:           serviceName,
			Spec:                  candidateSpec,
			BackupCleanupRequired: true,
			BackupContainerName:   backupName,
			Message:               fmt.Sprintf("Workload '%s' adopted successfully, but backup container %s could not be removed automatically; manual cleanup recommended.", serviceName, backupName),
		}, nil
	}

	return &AdoptionResult{
		Success:     true,
		ServiceName: serviceName,
		Spec:        candidateSpec,
		Message:     fmt.Sprintf("Workload '%s' successfully adopted into Podder.", serviceName),
	}, nil
}

func isOwnershipLabel(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(k, "io.podder.") ||
		strings.HasPrefix(k, "com.docker.compose.") ||
		strings.HasPrefix(k, "io.podman.compose.") ||
		k == "podman_systemd_unit" || k == "io.systemd.unit"
}

func (p *PodmanService) findContainerForAdoption(containerID string) (*Container, error) {
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect local containers: %w", err)
	}
	target := findContainerByIdentity(containers, containerID)
	if target == nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}
	return target, nil
}
