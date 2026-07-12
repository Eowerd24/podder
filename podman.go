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

// Container represents a Podman container.
type Container struct {
	Id         string   `json:"Id"`
	Names      []string `json:"Names"`
	Image      string   `json:"Image"`
	ImageID    string   `json:"ImageID"`
	State      string   `json:"State"`
	Status     string   `json:"Status"`
	Created    int64    `json:"Created"`
	ExitCode   int      `json:"ExitCode"`
	Command    []string `json:"Command"`
	AutoRemove bool     `json:"AutoRemove"`
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

	var containers []Container
	if strings.TrimSpace(stdout) == "" || strings.TrimSpace(stdout) == "[]" {
		return containers, nil
	}

	if err := json.Unmarshal([]byte(stdout), &containers); err != nil {
		return nil, fmt.Errorf("failed to parse containers json: %v", err)
	}

	return containers, nil
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
func (p *PodmanService) RunContainer(image string, name string, ports string, cmd string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("image name cannot be empty")
	}

	args := []string{"run", "-d"}

	name = strings.TrimSpace(name)
	if name != "" {
		args = append(args, "--name", name)
	}

	ports = strings.TrimSpace(ports)
	if ports != "" {
		args = append(args, "-p", ports)
	}

	args = append(args, image)

	cmd = strings.TrimSpace(cmd)
	if cmd != "" {
		// Split cmd by space to pass arguments properly (simple split, directly as slice elements)
		cmdParts := strings.Fields(cmd)
		args = append(args, cmdParts...)
	}

	_, stderr, err := p.runCommand(args...)
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
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
