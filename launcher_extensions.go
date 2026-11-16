package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type GUIViewerDefinition struct {
	Provider                string `json:"provider"`
	SessionIdentity         string `json:"sessionIdentity"`
	AuthenticationConfirmed bool   `json:"authenticationConfirmed"`
}
type TerminalDefinition struct {
	Program string   `json:"program"`
	Command []string `json:"command"`
}

func validateGUIViewer(d LauncherDefinition) error {
	if d.GUI == nil || d.GUI.Provider != "xpra-html5" || strings.TrimSpace(d.GUI.SessionIdentity) == "" || len(d.GUI.SessionIdentity) > 128 || !d.GUI.AuthenticationConfirmed {
		return fmt.Errorf("configure an existing authenticated Xpra HTML5 session and its identity")
	}
	return nil
}
func terminalCommandArgs(def TerminalDefinition, containerID string) (string, []string, error) {
	if err := validateFullContainerID(containerID); err != nil {
		return "", nil, err
	}
	if len(def.Command) == 0 || len(def.Command) > 64 {
		return "", nil, fmt.Errorf("configure explicit terminal command arguments")
	}
	for _, arg := range def.Command {
		if strings.ContainsRune(arg, 0) {
			return "", nil, fmt.Errorf("terminal command contains NUL")
		}
	}
	args := []string{"podman", "exec", "-it", "--", containerID}
	args = append(args, def.Command...)
	switch def.Program {
	case "x-terminal-emulator", "konsole":
		return def.Program, append([]string{"-e"}, args...), nil
	case "gnome-terminal":
		return def.Program, append([]string{"--"}, args...), nil
	case "wt.exe":
		return def.Program, append([]string{"--"}, args...), nil
	}
	return "", nil, fmt.Errorf("choose a supported terminal program")
}
func (p *PodmanService) OpenGUIEndpoint(containerID, endpointID string) (*OpenWebResult, error) {
	o, err := p.GetLauncherOptions(containerID)
	if err != nil {
		return nil, err
	}
	if o.Definition == nil || o.Definition.Kind != "gui" {
		return nil, fmt.Errorf("GUI viewer is not configured")
	}
	if err := validateGUIViewer(*o.Definition); err != nil {
		return nil, err
	}
	return p.OpenWebEndpoint(containerID, endpointID)
}

// Explicit per-invocation consent; metadata alone never executes a host program.
func (p *PodmanService) OpenConfiguredTerminal(containerID string, confirmed bool) error {
	if err := p.ensureLocalContext(true); err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("explicit terminal execution consent is required")
	}
	c, err := p.launcherContainer(containerID)
	if err != nil {
		return err
	}
	if c.State != "running" {
		return fmt.Errorf("container is not running")
	}
	root, err := openLauncherRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	d, err := readLauncher(root, p.launcherAssociation(*c).Key)
	if err != nil {
		return err
	}
	if d.Kind != "terminal" || d.Terminal == nil {
		return fmt.Errorf("terminal action is not configured")
	}
	name, args, err := terminalCommandArgs(*d.Terminal, c.Id)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		return fmt.Errorf("native macOS terminal integration is not available; use an explicit podman exec command")
	}
	path, err := p.lookPathFn()(name)
	if err != nil {
		return fmt.Errorf("terminal is unavailable: %s", name)
	}
	if p.terminalStart != nil {
		return p.terminalStart(path, args...)
	}
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
