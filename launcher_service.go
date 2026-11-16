package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func (p *PodmanService) launcherAssociation(c Container) LauncherAssociation {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	a := LauncherAssociation{Key: "container:" + c.Id, ContainerID: c.Id, ContainerName: name}
	// Container labels alone cannot make a stable managed identity authoritative.
	if c.Provenance.Type == "podder" {
		service := c.Labels["io.podder.service"]
		spec, err := p.GetSpec(service)
		if err == nil && spec.Managed && len(ValidateSpec(*spec)) == 0 && containerMatchesSpecLabels(&c, *spec) && name == spec.Name && sameImageIdentity(c.ImageID, spec.ResolvedImage) {
			a.Key = "service:" + service
		}
	}
	return a
}
func (p *PodmanService) launcherOptions(c Container) LauncherOptions {
	return p.launcherOptionsFor(c, p.launcherAssociation(c))
}
func (p *PodmanService) launcherOptionsFor(c Container, a LauncherAssociation) LauncherOptions {
	o := LauncherOptions{Association: a, Running: strings.EqualFold(c.State, "running"), Mappings: launcherMappings(c.PortMappings), Endpoints: []LauncherEndpointOption{}, Stale: []LauncherDefinition{}}
	o.Message = "No launchable interface configured. Published TCP ports do not prove HTTP."
	root, err := openLauncherRoot()
	if err != nil {
		o.Message = "Launcher storage unavailable: " + err.Error()
		return o
	}
	defer root.Close()
	d, err := readLauncher(root, o.Association.Key)
	if err == nil {
		o.Definition = d
		endpoints, validationErr := validateLauncher(*d, c.PortMappings)
		if validationErr != nil {
			o.Message = validationErr.Error()
			return o
		}
		o.Endpoints = endpoints
		switch d.Kind {
		case "none":
			o.Message = "No launchable interface configured (background service)."
		case "gui":
			if d.GUI == nil {
				o.Message = "Configure a provisioned authenticated Xpra HTML5 viewer."
			} else {
				o.Message = "Configured authenticated Xpra viewer; provider remains externally owned."
			}
		case "terminal":
			if d.Terminal == nil {
				o.Message = "No terminal command configured."
			} else {
				o.Message = "Explicit terminal command configured; consent is required on each launch."
			}
		case "web":
			o.Message = "Configured web endpoint. Availability is checked when opening."
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		o.Message = "Launcher settings need attention: " + err.Error()
	}
	// Name is only a hint for explicit re-association, never an authorization.
	var files *os.File
	if d == nil {
		files, _ = root.Open(".")
	}
	if files != nil {
		defer files.Close()
		entries, _ := files.ReadDir(-1)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == launcherFile(o.Association.Key) {
				continue
			}
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
				continue
			}
			data, e := root.ReadFile(entry.Name())
			if e != nil {
				continue
			}
			var previous LauncherDefinition
			if json.Unmarshal(data, &previous) == nil && launcherFile(previous.Association.Key) == entry.Name() && previous.SchemaVersion == launcherSchemaVersion && strings.HasPrefix(previous.Association.Key, "remote:") == strings.HasPrefix(o.Association.Key, "remote:") && previous.Association.ContainerName == o.Association.ContainerName && previous.Association.ContainerID != c.Id {
				o.Stale = append(o.Stale, previous)
			}
		}
	}
	if d == nil && len(o.Stale) > 0 {
		o.Message = "Settings for an older container exist. Review and re-associate explicitly."
	}
	if !o.Running {
		o.Message = "Container is stopped. Start it before opening. " + o.Message
	}
	return o
}

func (p *PodmanService) launcherContainer(id string) (*Container, error) {
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, err
	}
	// Exact full runtime identity, never a reusable short name/prefix.
	for _, c := range containers {
		if c.Id == id {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("container no longer exists; refresh the list")
}
func (p *PodmanService) GetLauncherOptions(containerID string) (*LauncherOptions, error) {
	if err := p.ensureLocalContext(false); err != nil {
		return nil, err
	}
	c, err := p.launcherContainer(containerID)
	if err != nil {
		return nil, err
	}
	o := p.launcherOptions(*c)
	return &o, nil
}

// One runtime read for the whole card list, rather than one process per card.
func (p *PodmanService) GetLauncherOverview() (map[string]LauncherOptions, error) {
	if err := p.ensureLocalContext(false); err != nil {
		return nil, err
	}
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, err
	}
	out := map[string]LauncherOptions{}
	for _, c := range containers {
		out[c.Id] = p.launcherOptions(c)
	}
	return out, nil
}
func (p *PodmanService) SaveLauncherDefinition(containerID string, definition LauncherDefinition) error {
	if err := p.ensureLocalContext(false); err != nil {
		return err
	}
	c, err := p.launcherContainer(containerID)
	if err != nil {
		return err
	}
	association := p.launcherAssociation(*c)
	if definition.Association != association {
		return fmt.Errorf("container association changed; reopen application settings")
	}
	if _, err := validateLauncher(definition, c.PortMappings); err != nil {
		return err
	}
	return writeLauncher(definition)
}

type OpenWebResult struct {
	URL     string `json:"url"`
	Opened  bool   `json:"opened"`
	Message string `json:"message"`
}

func (p *PodmanService) resolvedWebEndpoint(containerID, endpointID string, requireRunning bool) (string, *LauncherDefinition, error) {
	if err := p.ensureLocalContext(false); err != nil {
		return "", nil, err
	}
	c, err := p.launcherContainer(containerID)
	if err != nil {
		return "", nil, err
	}
	if requireRunning && !strings.EqualFold(c.State, "running") {
		return "", nil, fmt.Errorf("container is not running")
	}
	root, err := openLauncherRoot()
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	d, err := readLauncher(root, p.launcherAssociation(*c).Key)
	if err != nil {
		return "", nil, err
	}
	endpoints, err := validateLauncher(*d, c.PortMappings)
	if err != nil {
		return "", nil, err
	}
	for _, e := range endpoints {
		if e.ID == endpointID {
			return e.URL, d, nil
		}
	}
	return "", nil, fmt.Errorf("endpoint is not configured")
}
func probeWeb(ctx context.Context, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	// Authentication and redirects still establish an HTTP(S) interface.
	if response.StatusCode >= 500 {
		return fmt.Errorf("HTTP endpoint reports %s", response.Status)
	}
	return nil
}
func (p *PodmanService) checkWeb(ctx context.Context, target string) error {
	if p.webProbe != nil {
		return p.webProbe(ctx, target)
	}
	return probeWeb(ctx, target)
}
func (p *PodmanService) openWeb(target string) OpenWebResult {
	result := OpenWebResult{URL: target}
	opener := p.browserOpen
	if opener == nil {
		opener = func(target string) error {
			app := application.Get()
			if app == nil {
				return fmt.Errorf("browser integration unavailable")
			}
			return app.Browser.OpenURL(target)
		}
	}
	if err := opener(target); err != nil {
		result.Message = "Browser could not be opened. Copy the URL instead."
		return result
	}
	result.Opened = true
	result.Message = "Sent to the default browser."
	return result
}
func (p *PodmanService) GetWebEndpointURL(containerID, endpointID string) (string, error) {
	target, _, err := p.resolvedWebEndpoint(containerID, endpointID, true)
	return target, err
}
func (p *PodmanService) OpenWebEndpoint(containerID, endpointID string) (*OpenWebResult, error) {
	target, _, err := p.resolvedWebEndpoint(containerID, endpointID, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.checkWeb(ctx, target); err != nil {
		return &OpenWebResult{URL: target, Message: "Endpoint did not respond as HTTP(S). Check the application or copy the URL."}, nil
	}
	current, _, err := p.resolvedWebEndpoint(containerID, endpointID, true)
	if err != nil {
		return nil, err
	}
	if current != target {
		return nil, fmt.Errorf("endpoint changed while checking availability; review application settings")
	}
	result := p.openWeb(target)
	return &result, nil
}
func (p *PodmanService) StartAndOpenWebEndpoint(containerID, endpointID string) (*OpenWebResult, error) {
	target, d, err := p.resolvedWebEndpoint(containerID, endpointID, false)
	if err != nil {
		return nil, err
	}
	if !d.OpenAfterStart {
		return nil, fmt.Errorf("Start & Open is not enabled")
	}
	if err := p.StartContainer(containerID); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if p.checkWeb(ctx, target) == nil {
			current, _, err := p.resolvedWebEndpoint(containerID, endpointID, true)
			if err != nil {
				return nil, err
			}
			if current != target {
				return nil, fmt.Errorf("endpoint changed while starting; review application settings")
			}
			result := p.openWeb(target)
			return &result, nil
		}
		select {
		case <-ctx.Done():
			return &OpenWebResult{URL: target, Message: "Container was started, but its HTTP(S) endpoint was not ready within 15 seconds. Check Logs or copy the URL."}, nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}
