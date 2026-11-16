package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func exportRecipe(spec DisposableSessionSpec) (string, error) {
	if err := validateSessionSource(spec); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(savedSessionSpec(spec), "", "  ")
	return string(data), err
}

func (p *PodmanService) ImportSessionRecipe(content string) (*DisposableSessionSpec, error) {
	if len(content) > 65536 {
		return nil, fmt.Errorf("recipe exceeds size limit")
	}
	var spec DisposableSessionSpec
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("invalid recipe JSON")
	}
	if len(spec.Env) > 0 {
		return nil, fmt.Errorf("import cannot contain environment values; use requiredEnv and re-enter values at launch")
	}
	// Imported configuration is reviewable, but consent never travels with it.
	spec.AllowExternalBinds = false
	spec.AllowNetworkExposure = false
	if spec.GitHub != nil {
		if err := validateGitHubSource(*spec.GitHub); err != nil {
			return nil, err
		}
	}
	check := spec
	check.AllowExternalBinds = true
	check.AllowNetworkExposure = true
	check.RequiredEnv = nil
	check.RequireCommand = false
	if err := validateSessionSpec(check); err != nil {
		return nil, err
	}
	if spec.Policy == "temporary" {
		return nil, fmt.Errorf("an imported reusable recipe needs a retained policy")
	}
	// Import is a reviewable draft; saving remains an explicit separate action.
	return &spec, nil
}
func (p *PodmanService) SetSessionRecipeFavorite(name string, favorite bool) error {
	recipes, err := p.ListSessionRecipes()
	if err != nil {
		return err
	}
	for _, spec := range recipes {
		if spec.Name == name {
			spec.Favorite = favorite
			return saveSessionRecipe(spec)
		}
	}
	return fmt.Errorf("recipe not found")
}
func (p *PodmanService) RelaunchSessionRecipe(name string, env map[string]string, command []string) (*SessionRuntimeState, error) {
	return p.RelaunchSessionRecipeWithConsent(name, env, command, SessionLaunchConsent{})
}

// Consent is invocation material, never inherited from a saved recipe/journal.
type SessionLaunchConsent struct {
	AllowExternalBinds    bool                  `json:"allowExternalBinds"`
	AllowNetworkExposure  bool                  `json:"allowNetworkExposure"`
	ExternalBindApprovals []SessionBindApproval `json:"externalBindApprovals,omitempty"`
}

func (p *PodmanService) RelaunchSessionRecipeWithConsent(name string, env map[string]string, command []string, consent SessionLaunchConsent) (*SessionRuntimeState, error) {
	recipes, err := p.ListSessionRecipes()
	if err != nil {
		return nil, err
	}
	for _, spec := range recipes {
		if spec.Name == name {
			spec.AllowExternalBinds = consent.AllowExternalBinds
			spec.AllowNetworkExposure = consent.AllowNetworkExposure
			spec.ExternalBindApprovals = consent.ExternalBindApprovals
			spec.Env = env
			if len(command) > 0 {
				spec.Command = command
			}
			return p.LaunchDisposableSession(spec)
		}
	}
	return nil, fmt.Errorf("recipe not found")
}
func (p *PodmanService) SessionStarterProfiles() []DisposableSessionSpec {
	// Presets describe explicit app paths; none are installed or launched automatically.
	return []DisposableSessionSpec{{SchemaVersion: 1, Preset: "olivetin-hello", Name: "olivetin", Image: "docker.io/jamesread/olivetin:latest", Policy: "configuration", PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 49180, ContainerPort: 1337, Protocol: "tcp"}}, Config: SessionMount{Target: "/config"}, Web: &WebEndpoint{ID: "web", Scheme: "http", Mapping: PortMapping{HostIP: "127.0.0.1", HostPort: 49180, ContainerPort: 1337, Protocol: "tcp"}, Path: "/"}}}
}
