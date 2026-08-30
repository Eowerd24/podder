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
type PodmanService struct{}

var supportedImageExtensions = map[string]struct{}{
	".avif":  {},
	".bmp":   {},
	".gif":   {},
	".jpeg":  {},
	".jpg":   {},
	".png":   {},
	".svg":   {},
	".tif":   {},
	".tiff":  {},
	".webp":  {},
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
	cmd := exec.Command("podman", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
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

// RunContainer runs a container from an image with optional configuration.
func (p *PodmanService) RunContainer(image string, name string, ports string, cmd string, hostPath string, containerPath string, readOnly bool) error {
	args, err := buildRunContainerArgs(image, name, ports, cmd, hostPath, containerPath, readOnly)
	if err != nil {
		return err
	}

	_, stderr, err := p.runCommand(args...)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

// RunContainerWithPortMappings runs a container from an image with structured port mappings.
func (p *PodmanService) RunContainerWithPortMappings(image string, name string, portMappings []PortMapping, cmd string, hostPath string, containerPath string, readOnly bool) error {
	args, err := buildRunContainerArgsWithMappings(image, name, portMappings, cmd, hostPath, containerPath, readOnly)
	if err != nil {
		return err
	}

	_, stderr, err := p.runCommand(args...)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}

	// Auto-persist declarative spec if a workload name was provided
	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		var binds []BindMountSpec
		if hostPath != "" && containerPath != "" {
			binds = append(binds, BindMountSpec{
				HostPath:      hostPath,
				ContainerPath: containerPath,
				ReadOnly:      readOnly,
			})
		}
		_ = p.SaveSpec(ContainerSpec{
			Name:         trimmedName,
			Image:        image,
			PortMappings: portMappings,
			Binds:        binds,
			Command:      cmd,
		})
	}

	return nil
}

func buildPortArg(m PortMapping) string {
	proto := strings.ToLower(strings.TrimSpace(m.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	hostIP := strings.TrimSpace(m.HostIP)
	if hostIP != "" && hostIP != "0.0.0.0" && hostIP != "*" {
		return fmt.Sprintf("%s:%d:%d/%s", hostIP, m.HostPort, m.ContainerPort, proto)
	}
	if m.HostPort != 0 {
		return fmt.Sprintf("%d:%d/%s", m.HostPort, m.ContainerPort, proto)
	}
	return fmt.Sprintf("%d/%s", m.ContainerPort, proto)
}

func buildRunContainerArgsWithMappings(image string, name string, portMappings []PortMapping, cmd string, hostPath string, containerPath string, readOnly bool) ([]string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, fmt.Errorf("image name cannot be empty")
	}

	args := []string{"run", "-d"}

	name = strings.TrimSpace(name)
	if name != "" {
		args = append(args, "--name", name)
		args = append(args, "--label", "io.podder.managed=true")
		args = append(args, "--label", "io.podder.service="+name)
	} else {
		args = append(args, "--label", "io.podder.managed=true")
	}

	for _, m := range portMappings {
		if m.ContainerPort > 0 {
			args = append(args, "-p", buildPortArg(m))
		}
	}

	hostPath = strings.TrimSpace(hostPath)
	containerPath = strings.TrimSpace(containerPath)
	if hostPath == "" && containerPath != "" {
		return nil, fmt.Errorf("host path is required when a container mount path is provided")
	}
	if hostPath != "" && containerPath == "" {
		return nil, fmt.Errorf("container mount path is required when a host path is provided")
	}
	if hostPath != "" {
		if _, err := os.Stat(hostPath); err != nil {
			return nil, fmt.Errorf("host path is not accessible: %w", err)
		}

		mountSpec := fmt.Sprintf("type=bind,src=%s,target=%s", hostPath, containerPath)
		if readOnly {
			mountSpec += ",readonly"
		}
		args = append(args, "--mount", mountSpec)
	}

	args = append(args, image)

	cmd = strings.TrimSpace(cmd)
	if cmd != "" {
		args = append(args, strings.Fields(cmd)...)
	}

	return args, nil
}

func buildRunContainerArgs(image string, name string, ports string, cmd string, hostPath string, containerPath string, readOnly bool) ([]string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, fmt.Errorf("image name cannot be empty")
	}

	args := []string{"run", "-d"}

	name = strings.TrimSpace(name)
	if name != "" {
		args = append(args, "--name", name)
	}

	ports = strings.TrimSpace(ports)
	if ports != "" {
		portItems := strings.FieldsFunc(ports, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';'
		})
		if len(portItems) > 0 {
			for _, pItem := range portItems {
				pTrimmed := strings.TrimSpace(pItem)
				if pTrimmed != "" {
					args = append(args, "-p", pTrimmed)
				}
			}
		} else {
			args = append(args, "-p", ports)
		}
	}

	hostPath = strings.TrimSpace(hostPath)
	containerPath = strings.TrimSpace(containerPath)
	if hostPath == "" && containerPath != "" {
		return nil, fmt.Errorf("host path is required when a container mount path is provided")
	}
	if hostPath != "" && containerPath == "" {
		return nil, fmt.Errorf("container mount path is required when a host path is provided")
	}
	if hostPath != "" {
		if _, err := os.Stat(hostPath); err != nil {
			return nil, fmt.Errorf("host path is not accessible: %w", err)
		}

		mountSpec := fmt.Sprintf("type=bind,src=%s,target=%s", hostPath, containerPath)
		if readOnly {
			mountSpec += ",readonly"
		}
		args = append(args, "--mount", mountSpec)
	}

	args = append(args, image)

	cmd = strings.TrimSpace(cmd)
	if cmd != "" {
		args = append(args, strings.Fields(cmd)...)
	}

	return args, nil
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

	var composeCmd *exec.Cmd
	if _, err := exec.LookPath("podman-compose"); err == nil {
		if action == "up" {
			composeCmd = exec.Command("podman-compose", "up", "-d")
		} else {
			composeCmd = exec.Command("podman-compose", "down")
		}
	} else if _, err := exec.LookPath("docker-compose"); err == nil {
		if action == "up" {
			composeCmd = exec.Command("docker-compose", "up", "-d")
		} else {
			composeCmd = exec.Command("docker-compose", "down")
		}
	} else {
		// Fallback to "podman compose"
		if action == "up" {
			composeCmd = exec.Command("podman", "compose", "up", "-d")
		} else {
			composeCmd = exec.Command("podman", "compose", "down")
		}
	}

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
