package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// PodmanService handles execution of Podman CLI commands and parsing of JSON outputs.
type PodmanService struct {
	// runner executes external commands. The zero value (nil) falls back to
	// the real host executor in production; tests inject a scripted fake so
	// every failure point of a transaction can be exercised deterministically.
	runner CommandRunner
	// lookPath resolves which compose provider binary is on PATH. The zero
	// value (nil) falls back to exec.LookPath in production; tests inject a
	// scripted fake so Compose mutation is exercisable without a real
	// podman-compose/docker-compose install.
	lookPath lookPathFunc
}

// cmdRunner returns the configured CommandRunner, falling back to the real
// host executor when none was injected.
func (p *PodmanService) cmdRunner() CommandRunner {
	if p.runner != nil {
		return p.runner
	}
	return defaultCommandRunner
}

// lookPathFn returns the configured lookPathFunc, falling back to the real
// exec.LookPath when none was injected.
func (p *PodmanService) lookPathFn() lookPathFunc {
	if p.lookPath != nil {
		return p.lookPath
	}
	return exec.LookPath
}

var supportedImageExtensions = map[string]struct{}{
	".avif": {},
	".bmp":  {},
	".gif":  {},
	".jpeg": {},
	".jpg":  {},
	".png":  {},
	".svg":  {},
	".tif":  {},
	".tiff": {},
	".webp": {},
}

// rawPodmanPort represents port structure in raw Podman JSON.
type rawPodmanPort struct {
	HostIP        string `json:"host_ip"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Range         int    `json:"range"`
	Protocol      string `json:"protocol"`
}

// rawContainer represents a raw container from Podman JSON.
type rawContainer struct {
	Id         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	ImageID    string            `json:"ImageID"`
	State      string            `json:"State"`
	Status     string            `json:"Status"`
	Created    int64             `json:"Created"`
	ExitCode   int               `json:"ExitCode"`
	Command    []string          `json:"Command"`
	AutoRemove bool              `json:"AutoRemove"`
	Ports      []rawPodmanPort   `json:"Ports"`
	Pod        string            `json:"Pod"`
	PodName    string            `json:"PodName"`
	Labels     map[string]string `json:"Labels"`
}

// Container represents a Podman container with structured port mappings and provenance.
type Container struct {
	Id           string             `json:"Id"`
	Names        []string           `json:"Names"`
	Image        string             `json:"Image"`
	ImageID      string             `json:"ImageID"`
	State        string             `json:"State"`
	Status       string             `json:"Status"`
	Created      int64              `json:"Created"`
	ExitCode     int                `json:"ExitCode"`
	Command      []string           `json:"Command"`
	AutoRemove   bool               `json:"AutoRemove"`
	PortMappings []PortMapping      `json:"PortMappings"`
	Ports        []PortMapping      `json:"Ports"`
	Pod          string             `json:"Pod,omitempty"`
	PodName      string             `json:"PodName,omitempty"`
	Labels       map[string]string  `json:"Labels,omitempty"`
	Provenance   WorkloadProvenance `json:"provenance"`
}

// Image represents a Podman image.
type Image struct {
	Id         string   `json:"Id"`
	Names      []string `json:"Names"`
	Digest     string   `json:"Digest"`
	Size       int64    `json:"Size"`
	CreatedAt  string   `json:"CreatedAt"`
	Containers int      `json:"Containers"`
}

// SystemInfo represents high-level host and store statistics.
type SystemInfo struct {
	PodmanVersion     string `json:"podmanVersion"`
	OS                string `json:"os"`
	Kernel            string `json:"kernel"`
	Distribution      string `json:"distribution"`
	CPUs              int    `json:"cpus"`
	MemTotal          int64  `json:"memTotal"`
	MemFree           int64  `json:"memFree"`
	TotalContainers   int    `json:"totalContainers"`
	RunningContainers int    `json:"runningContainers"`
	StoppedContainers int    `json:"stoppedContainers"`
	TotalImages       int    `json:"totalImages"`
	Uptime            string `json:"uptime"`
}

// runCommand runs a Podman command with arguments.
func (p *PodmanService) runCommand(args ...string) (string, string, error) {
	return p.cmdRunner().Run("podman", args...)
}

// GetSystemInfo fetches information about the Podman host and storage.
func (p *PodmanService) GetSystemInfo() (*SystemInfo, error) {
	stdout, stderr, err := p.runCommand("info", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to run podman info: %v, stderr: %s", err, stderr)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse podman info json: %v", err)
	}

	info := &SystemInfo{}

	// Safely parse host details
	if host, ok := raw["host"].(map[string]interface{}); ok {
		if osVal, ok := host["os"].(string); ok {
			info.OS = osVal
		}
		if kernelVal, ok := host["kernel"].(string); ok {
			info.Kernel = kernelVal
		}
		if cpusVal, ok := host["cpus"].(float64); ok {
			info.CPUs = int(cpusVal)
		}
		if memTotalVal, ok := host["memTotal"].(float64); ok {
			info.MemTotal = int64(memTotalVal)
		}
		if memFreeVal, ok := host["memFree"].(float64); ok {
			info.MemFree = int64(memFreeVal)
		}
		if uptimeVal, ok := host["uptime"].(string); ok {
			info.Uptime = uptimeVal
		}
		if dist, ok := host["distribution"].(map[string]interface{}); ok {
			distName, _ := dist["distribution"].(string)
			distVer, _ := dist["version"].(string)
			info.Distribution = fmt.Sprintf("%s %s", distName, distVer)
		}
	}

	// Safely parse store details
	if store, ok := raw["store"].(map[string]interface{}); ok {
		if cStore, ok := store["containerStore"].(map[string]interface{}); ok {
			if num, ok := cStore["number"].(float64); ok {
				info.TotalContainers = int(num)
			}
			if run, ok := cStore["running"].(float64); ok {
				info.RunningContainers = int(run)
			}
			if stopped, ok := cStore["stopped"].(float64); ok {
				info.StoppedContainers = int(stopped)
			}
		}
		if iStore, ok := store["imageStore"].(map[string]interface{}); ok {
			if num, ok := iStore["number"].(float64); ok {
				info.TotalImages = int(num)
			}
		}
	}

	// Safely parse version
	if version, ok := raw["version"].(map[string]interface{}); ok {
		if ver, ok := version["Version"].(string); ok {
			info.PodmanVersion = ver
		}
	}

	return info, nil
}

// parseContainersJSON parses JSON from `podman ps --format json`.
func parseContainersJSON(data []byte) ([]Container, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "[]" {
		return []Container{}, nil
	}

	var rawList []rawContainer
	if err := json.Unmarshal(data, &rawList); err != nil {
		// Fallback: try unmarshaling directly into []Container
		var fallbackList []Container
		if errFallback := json.Unmarshal(data, &fallbackList); errFallback == nil {
			for i := range fallbackList {
				if len(fallbackList[i].PortMappings) == 0 && len(fallbackList[i].Ports) > 0 {
					fallbackList[i].PortMappings = fallbackList[i].Ports
				} else if len(fallbackList[i].Ports) == 0 && len(fallbackList[i].PortMappings) > 0 {
					fallbackList[i].Ports = fallbackList[i].PortMappings
				}
			}
			return fallbackList, nil
		}
		return nil, fmt.Errorf("failed to parse containers json: %w", err)
	}

	containers := make([]Container, len(rawList))
	for i, rc := range rawList {
		var mappings []PortMapping
		for _, rp := range rc.Ports {
			proto := strings.ToLower(rp.Protocol)
			if proto == "" {
				proto = "tcp"
			}
			mappings = append(mappings, PortMapping{
				HostIP:        rp.HostIP,
				HostPort:      rp.HostPort,
				ContainerPort: rp.ContainerPort,
				Protocol:      proto,
				RangeSize:     rp.Range,
			})
		}

		podName := rc.PodName
		if podName == "" {
			podName = rc.Pod
		}

		provenance := ClassifyProvenance(rc.Labels, rc.Pod, rc.PodName)

		containers[i] = Container{
			Id:           rc.Id,
			Names:        rc.Names,
			Image:        rc.Image,
			ImageID:      rc.ImageID,
			State:        rc.State,
			Status:       rc.Status,
			Created:      rc.Created,
			ExitCode:     rc.ExitCode,
			Command:      rc.Command,
			AutoRemove:   rc.AutoRemove,
			PortMappings: mappings,
			Ports:        mappings,
			Pod:          rc.Pod,
			PodName:      podName,
			Labels:       rc.Labels,
			Provenance:   provenance,
		}
	}

	return containers, nil
}

// ListContainers lists all containers (running and stopped if all=true).
func (p *PodmanService) ListContainers(all bool) ([]Container, error) {
	args := []string{"ps", "--format", "json"}
	if all {
		args = append(args, "-a")
	}

	stdout, stderr, err := p.runCommand(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v, stderr: %s", err, stderr)
	}

	return parseContainersJSON([]byte(stdout))
}

// ListImages lists local images.
func (p *PodmanService) ListImages() ([]Image, error) {
	stdout, stderr, err := p.runCommand("images", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %v, stderr: %s", err, stderr)
	}

	var images []Image
	if strings.TrimSpace(stdout) == "" || strings.TrimSpace(stdout) == "[]" {
		return images, nil
	}

	if err := json.Unmarshal([]byte(stdout), &images); err != nil {
		return nil, fmt.Errorf("failed to parse images json: %v", err)
	}

	return images, nil
}

// StartContainer starts a container by ID or name.
func (p *PodmanService) StartContainer(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("container id cannot be empty")
	}
	_, stderr, err := p.runCommand("start", id)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// StopContainer stops a container by ID or name.
func (p *PodmanService) StopContainer(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("container id cannot be empty")
	}
	_, stderr, err := p.runCommand("stop", id)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// RestartContainer restarts a container by ID or name.
func (p *PodmanService) RestartContainer(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("container id cannot be empty")
	}
	_, stderr, err := p.runCommand("restart", id)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// RemoveContainer forces the removal of a container by ID or name.
func (p *PodmanService) RemoveContainer(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("container id cannot be empty")
	}
	_, stderr, err := p.runCommand("rm", "-f", id)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// RemoveImage forces the removal of an image by ID.
func (p *PodmanService) RemoveImage(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("image id cannot be empty")
	}
	_, stderr, err := p.runCommand("rmi", "-f", id)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// GetContainerLogs returns logs for a container (last 200 lines).
func (p *PodmanService) GetContainerLogs(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("container id cannot be empty")
	}
	stdout, stderr, err := p.runCommand("logs", "--tail", "200", id)
	if err != nil {
		// Sometimes logs might write to stderr, so if we get a standard exit code we can combine or return stderr.
		return stdout, fmt.Errorf("failed to get logs: %v, stderr: %s", err, stderr)
	}
	// Return both stdout and stderr since logs might write to either.
	return stdout + stderr, nil
}

// PullImage pulls an image from a registry.
func (p *PodmanService) PullImage(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("image name cannot be empty")
	}
	_, stderr, err := p.runCommand("pull", name)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// ContainerCreateRequest is the full request for creating a new container.
// Managed explicitly controls whether the workload becomes Podder-managed —
// it is never inferred from the presence of port mappings or any other
// field.
type ContainerCreateRequest struct {
	Image        string            `json:"image"`
	Name         string            `json:"name"`
	Managed      bool              `json:"managed"`
	PortMappings []PortMapping     `json:"portMappings"`
	Binds        []BindMountSpec   `json:"binds"`
	Env          map[string]string `json:"env"`
	// Command accepts either a JSON array (preferred) or a single
	// shell-style string (tokenized via SplitShellCommand, e.g. from a
	// free-text "Command" field in the Run Container UI) — see
	// CommandArgv's UnmarshalJSON.
	Command    CommandArgv `json:"command"`
	Entrypoint []string    `json:"entrypoint"`
}

// ContainerCreateResult reports the outcome of a container creation request.
type ContainerCreateResult struct {
	Success     bool   `json:"success"`
	ContainerID string `json:"containerId"`
	Managed     bool   `json:"managed"`
	Message     string `json:"message,omitempty"`
}

// CreateContainer creates a new container from an explicit request. It is
// the single entry point for both managed and unmanaged creation:
//
//  1. validate the requested spec (pure, local checks)
//  2. run final backend validation on every port mapping (registry/runtime
//     collisions cannot be bypassed by calling this method directly, and
//     mappings are checked against each other within the same request)
//  3. build the exact run arguments via BuildRunArgsFromSpec
//  4. if Managed, persist a candidate spec BEFORE creating the container
//  5. create the container
//  6. verify it exists with the expected identity
//  7. commit the candidate spec (rename into place) — a container is never
//     left labeled io.podder.managed=true without a matching authoritative
//     spec on disk, and a spec is never committed for a container that
//     doesn't exist
//
// Unmanaged creation remains fully supported: Managed=false simply skips
// steps 4 and 7, and no io.podder.managed label is ever applied.
func (p *PodmanService) CreateContainer(req ContainerCreateRequest) (*ContainerCreateResult, error) {
	spec := ContainerSpec{
		SchemaVersion: CurrentSpecSchemaVersion,
		Name:          strings.TrimSpace(req.Name),
		Image:         strings.TrimSpace(req.Image),
		Managed:       req.Managed,
		PortMappings:  req.PortMappings,
		Binds:         req.Binds,
		Env:           req.Env,
		Command:       req.Command,
		Entrypoint:    req.Entrypoint,
	}

	if errs := ValidateSpec(spec); len(errs) > 0 {
		return nil, fmt.Errorf("invalid container spec: %s", strings.Join(errs, "; "))
	}

	if err := p.validateMappingsForCreate(spec.PortMappings); err != nil {
		return nil, err
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to build run arguments: %w", err)
	}

	var candidatePath string
	if spec.Managed {
		candidatePath, err = writeCandidateSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("failed to persist candidate spec: %w", err)
		}
	}

	stdout, stderr, err := p.runCommand(args...)
	if err != nil {
		discardCandidateSpec(candidatePath)
		return nil, fmt.Errorf("failed to create container: %v (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	containerID := strings.TrimSpace(stdout)
	result := &ContainerCreateResult{ContainerID: containerID}

	if spec.Managed {
		if !p.containerExistsByIdentity(containerID, spec.Name) {
			discardCandidateSpec(candidatePath)
			_ = p.RemoveContainer(containerID)
			return nil, fmt.Errorf("container failed to verify after creation; managed state was not committed and the container was removed")
		}
		if err := commitCandidateSpec(candidatePath, spec); err != nil {
			discardCandidateSpec(candidatePath)
			_ = p.RemoveContainer(containerID)
			return nil, fmt.Errorf("failed to commit managed spec, container removed to avoid an unmanaged-but-labeled state: %w", err)
		}
	}

	result.Success = true
	result.Managed = spec.Managed
	result.Message = "Container created successfully."
	return result, nil
}

// containerExistsByIdentity verifies a just-created container is visible to
// Podman under the expected ID (or name), used as the "verify" step before
// committing managed state.
func (p *PodmanService) containerExistsByIdentity(containerID, name string) bool {
	containers, err := p.ListContainers(true)
	if err != nil {
		return false
	}
	for _, c := range containers {
		if c.Id == containerID || (containerID != "" && strings.HasPrefix(c.Id, containerID)) {
			return true
		}
		if name != "" {
			for _, n := range c.Names {
				if strings.TrimPrefix(n, "/") == name {
					return true
				}
			}
		}
	}
	return false
}

// validateMappingsForCreate performs the final, mandatory backend validation
// of every requested port mapping immediately before it is ever handed to
// Podman: each mapping must independently pass ValidatePortMapping (which
// checks registry reservations and live runtime collisions), and mappings
// must not conflict with one another within the same request. This cannot
// be bypassed by calling CreateContainer directly through the Wails bridge —
// frontend validation is advisory only.
func (p *PodmanService) validateMappingsForCreate(mappings []PortMapping) error {
	var seen []PortClaim
	for _, m := range mappings {
		req := PortMappingRequest{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort,
			ContainerPort: m.ContainerPort,
			Protocol:      m.Protocol,
		}
		valResult, err := p.ValidatePortMapping(req)
		if err != nil {
			return fmt.Errorf("failed to validate port mapping %s: %w", m.DisplayString(), err)
		}
		if valResult == nil || !valResult.Valid {
			msg := "invalid port mapping"
			if valResult != nil {
				for _, c := range valResult.Checks {
					if !c.Passed {
						msg = c.Message
						break
					}
				}
			}
			return fmt.Errorf("port mapping %s rejected: %s", m.DisplayString(), msg)
		}

		if m.HostPort != 0 {
			candidate := PortClaim{Address: m.HostIP, Port: m.HostPort, Protocol: m.Protocol, RangeSize: m.RangeSize}
			if conflict := FindConflict(seen, candidate, ""); conflict != nil {
				return fmt.Errorf("port mapping %s conflicts with another mapping in this same request", m.DisplayString())
			}
			seen = append(seen, candidate)
		}
	}
	return nil
}

func isSupportedImageFile(path string) bool {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	_, ok := supportedImageExtensions[extension]
	return ok
}

// SelectHostPath prompts the user to select a host folder or image file for a bind mount.
func (p *PodmanService) SelectHostPath(kind string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))

	dialog := application.Get().Dialog.OpenFile()
	switch kind {
	case "folder":
		dialog = dialog.SetTitle("Select Host Folder").CanChooseDirectories(true).CanChooseFiles(false)
	case "image":
		dialog = dialog.SetTitle("Select Host Image File").CanChooseDirectories(false).CanChooseFiles(true)
	default:
		return "", fmt.Errorf("unsupported selection kind: %s", kind)
	}

	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("failed to open dialog: %v", err)
	}
	if path == "" {
		return "", nil
	}

	if kind == "image" && !isSupportedImageFile(path) {
		return "", fmt.Errorf("selected file must be an image (png, jpg, jpeg, gif, webp, bmp, svg, tif, tiff, avif)")
	}

	return path, nil
}

// SelectAndRunCompose triggers a native OS file dialog to select a folder or compose file,
// and then executes docker/podman compose in that directory.
func (p *PodmanService) SelectAndRunCompose(action string) (string, error) {
	dialog := application.Get().Dialog.OpenFile().
		SetTitle("Select Compose File or Directory").
		CanChooseDirectories(true).
		CanChooseFiles(true)

	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("failed to open dialog: %v", err)
	}
	if path == "" {
		return "Cancelled by user.", nil
	}

	// Determine if path is a file or directory
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat path: %v", err)
	}

	dir := path
	if !info.IsDir() {
		dir = filepath.Dir(path)
	}

	// Reuse the exact same provider resolution and readiness preflight as
	// CLI passthrough and Compose mutation, instead of a third hand-rolled
	// implementation with its own (previously buggy) argv construction.
	provider, err := resolveComposeProviderWithLookPath(action, p.lookPathFn())
	if err != nil {
		return "", err
	}
	if err := ensureComposeProviderReady(provider); err != nil {
		return "", err
	}

	verb, extra, err := composeVerbAndArgs(action)
	if err != nil {
		return "", err
	}

	composeCmd := exec.Command(provider.path, provider.BuildArgs("", verb, extra, "")...)
	composeCmd.Dir = dir

	output, err := composeCmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("compose error: %v\noutput: %s", err, string(output))
	}

	return string(output), nil
}

// BuildImageFromDirectory prompts the user for a directory, and runs podman build inside it.
func (p *PodmanService) BuildImageFromDirectory(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", fmt.Errorf("image tag cannot be empty")
	}

	dialog := application.Get().Dialog.OpenFile().
		SetTitle("Select Directory containing Dockerfile").
		CanChooseDirectories(true).
		CanChooseFiles(false)

	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("failed to open dialog: %v", err)
	}
	if path == "" {
		return "Cancelled by user.", nil
	}

	cmd := exec.Command("podman", "build", "-t", tag, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("build error: %v\noutput: %s", err, string(output))
	}
	return string(output), nil
}
