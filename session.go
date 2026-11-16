package main

import (
	"crypto/rand"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const sessionSchema = 1

var sessionIDPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)
var secretKeyPattern = regexp.MustCompile(`(?i)password|token|secret|api[_-]?key|private[_-]?key|credential`)

type SessionMount struct {
	Target    string `json:"target"`
	ReadWrite bool   `json:"readWrite"`
}
type SessionBindApproval struct {
	HostPath       string `json:"hostPath"`
	ContainerPath  string `json:"containerPath"`
	ReadOnly       bool   `json:"readOnly"`
	ObjectIdentity string `json:"objectIdentity"`
}
type GitHubSource struct {
	URL           string `json:"url"`
	Commit        string `json:"commit"`
	Subdirectory  string `json:"subdirectory"`
	Containerfile string `json:"containerfile"`
}
type DisposableSessionSpec struct {
	KeepID                bool                  `json:"keepID"`
	Preset                string                `json:"preset,omitempty"`
	LaunchID              string                `json:"launchID"`
	SchemaVersion         int                   `json:"schemaVersion"`
	Name                  string                `json:"name"`
	Image                 string                `json:"image"`
	Policy                string                `json:"policy"` // temporary, configuration, data
	PortMappings          []PortMapping         `json:"portMappings"`
	Command               []string              `json:"command"`
	Env                   map[string]string     `json:"env"`
	RequiredEnv           []string              `json:"requiredEnv"`
	RequireCommand        bool                  `json:"requireCommand"`
	SaveCommand           bool                  `json:"saveCommand"`
	Config                SessionMount          `json:"config"`
	Data                  SessionMount          `json:"data"`
	ExternalBinds         []BindMountSpec       `json:"externalBinds"`
	ExternalBindApprovals []SessionBindApproval `json:"externalBindApprovals,omitempty"`
	AllowNetworkExposure  bool                  `json:"allowNetworkExposure"`
	AllowExternalBinds    bool                  `json:"allowExternalBinds"`
	Web                   *WebEndpoint          `json:"web"`
	GitHub                *GitHubSource         `json:"github,omitempty"`
	Favorite              bool                  `json:"favorite"`
}
type SessionRuntimeState struct {
	LaunchOperation   string                `json:"launchOperation,omitempty"`
	InterruptedLaunch bool                  `json:"interruptedLaunch"`
	RuntimeIdentity   string                `json:"runtimeIdentity,omitempty"`
	BuildOperation    string                `json:"buildOperation,omitempty"`
	SchemaVersion     int                   `json:"schemaVersion"`
	ID                string                `json:"id"`
	Token             string                `json:"token"`
	State             string                `json:"state"`
	ContainerID       string                `json:"containerID"`
	ContainerName     string                `json:"containerName"`
	ImageID           string                `json:"imageID"`
	ImageTag          string                `json:"imageTag"`
	ImageOwned        bool                  `json:"imageOwned"`
	StartedAt         string                `json:"startedAt"`
	Spec              DisposableSessionSpec `json:"spec"`
	Message           string                `json:"message"`
	Retained          []string              `json:"retained"`
	BuildDefinition   string                `json:"buildDefinition"`
	BuildDigest       string                `json:"buildDigest"`
	BuildLog          string                `json:"buildLog"`
	InterruptedBuild  bool                  `json:"interruptedBuild"`
	BuildNetwork      bool                  `json:"buildNetwork"`
}
type SessionPreview struct {
	Argv    []string              `json:"argv"`
	Summary []string              `json:"summary"`
	Spec    DisposableSessionSpec `json:"spec"`
}

func validateSessionSpec(s DisposableSessionSpec) error {
	if err := validateSessionSource(s); err != nil {
		return err
	}
	if s.Preset != "" && s.Preset != "olivetin-hello" {
		return fmt.Errorf("unknown starter profile")
	}
	if s.LaunchID != "" && !sessionIDPattern.MatchString(s.LaunchID) {
		return fmt.Errorf("invalid launch ID")
	}
	if s.SchemaVersion != sessionSchema {
		return fmt.Errorf("unsupported session schema")
	}
	if err := validateServiceName(s.Name); err != nil {
		return err
	}
	if len(s.Name) > 80 {
		return fmt.Errorf("session name is too long")
	}
	if s.Policy != "temporary" && s.Policy != "configuration" && s.Policy != "data" {
		return fmt.Errorf("choose a persistence policy")
	}
	if strings.TrimSpace(s.Image) == "" || strings.HasPrefix(s.Image, "-") || strings.ContainsAny(s.Image, " \t\n\r\x00") {
		return fmt.Errorf("a valid image reference is required")
	}
	base := ContainerSpec{SchemaVersion: CurrentSpecSchemaVersion, Name: s.Name, Image: s.Image, PortMappings: s.PortMappings, Env: s.Env, Command: CommandArgv(s.Command), Binds: s.ExternalBinds}
	if errs := ValidateSpec(base); len(errs) > 0 {
		return fmt.Errorf("invalid session: %s", strings.Join(errs, "; "))
	}
	for _, m := range s.PortMappings {
		if m.HostPort == 0 {
			return fmt.Errorf("sessions require explicit host ports")
		}
		ip := net.ParseIP(m.HostIP)
		if ip == nil {
			return fmt.Errorf("sessions require an explicit host address (default 127.0.0.1)")
		}
		if !ip.IsLoopback() && !s.AllowNetworkExposure {
			return fmt.Errorf("LAN/wildcard binding requires explicit exposure approval")
		}
	}
	for _, m := range []SessionMount{s.Config, s.Data} {
		if m.Target != "" && (!filepath.IsAbs(m.Target) || filepath.Clean(m.Target) != m.Target || strings.ContainsAny(m.Target, ",\x00") || m.Target == "/" || sensitiveMountPath(m.Target)) {
			return fmt.Errorf("invalid application mount target")
		}
	}
	if s.Config.Target != "" && s.Config.Target == s.Data.Target {
		return fmt.Errorf("config and data must use different targets")
	}
	if len(s.ExternalBinds) > 0 && !s.AllowExternalBinds {
		return fmt.Errorf("external directories require explicit approval")
	}
	for _, b := range s.ExternalBinds {
		if sensitiveMountPath(b.HostPath) || filepath.Clean(b.HostPath) == string(filepath.Separator) {
			return fmt.Errorf("system directories and host sockets cannot be mounted")
		}
	}
	if s.Web != nil {
		d := LauncherDefinition{SchemaVersion: 1, Kind: "web", Endpoints: []WebEndpoint{*s.Web}}
		if _, err := validateLauncher(d, s.PortMappings); err != nil {
			return err
		}
	}
	for _, key := range s.RequiredEnv {
		if value, ok := s.Env[key]; !ok || value == "" {
			return fmt.Errorf("re-enter environment variable %s", key)
		}
	}
	if s.RequireCommand && len(s.Command) == 0 {
		return fmt.Errorf("re-enter the command argv for this recipe")
	}
	return nil
}
func validateSessionSource(s DisposableSessionSpec) error {
	for _, bind := range s.ExternalBinds {
		if bind.NoSuid || bind.NoDev {
			return fmt.Errorf("nosuid/nodev bind restrictions require managed spec schema 4; session schema 1 cannot preserve them")
		}
	}
	if s.GitHub != nil {
		return validateGitHubSource(*s.GitHub)
	}
	return nil
}

func sensitiveMountPath(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/etc", "/boot"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return strings.HasSuffix(clean, ".sock")
}
func newSessionID() string { return strings.ToLower(rand.Text()) }
func savedSessionSpec(s DisposableSessionSpec) DisposableSessionSpec {
	s.LaunchID = ""
	// Stored metadata is never current launch consent.
	s.AllowExternalBinds = false
	s.AllowNetworkExposure = false
	s.ExternalBindApprovals = nil
	keys := map[string]bool{}
	for _, key := range s.RequiredEnv {
		keys[key] = true
	}
	for key := range s.Env {
		keys[key] = true
	}
	s.RequiredEnv = nil
	for key := range keys {
		s.RequiredEnv = append(s.RequiredEnv, key)
	}
	sort.Strings(s.RequiredEnv)
	s.Env = nil
	if !s.SaveCommand {
		s.RequireCommand = s.RequireCommand || len(s.Command) > 0
		s.Command = nil
	}
	if s.GitHub != nil {
		copy := *s.GitHub
		s.GitHub = &copy
	}
	return s
}

// Adapt only disposable flags. All field translation remains in the shared builder.
func BuildSessionRunArgs(s DisposableSessionSpec, id string, binds []BindMountSpec) ([]string, error) {
	return buildSessionRunArgs(s, id, binds, true)
}
func buildSessionRunArgs(s DisposableSessionSpec, id string, binds []BindMountSpec, checkPaths bool) ([]string, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid session identity")
	}
	if err := validateSessionSpec(s); err != nil {
		return nil, err
	}
	base := ContainerSpec{SchemaVersion: CurrentSpecSchemaVersion, Name: s.Name + "-" + id[:8], Image: s.Image, PortMappings: s.PortMappings, Env: s.Env, Command: CommandArgv(s.Command), Binds: append(append([]BindMountSpec{}, s.ExternalBinds...), binds...)}
	args, err := buildRunArgsFromSpec(base, checkPaths)
	if err != nil {
		return nil, err
	}
	flags := []string{"--rm", "--label", "io.podder.session=true", "--label", "io.podder.session-id=" + id}
	if s.KeepID {
		flags = append(flags, "--userns=keep-id")
	}
	out := append([]string{}, args[:2]...)
	out = append(out, flags...)
	return append(out, args[2:]...), nil
}
func maskedSessionArgs(args []string, s DisposableSessionSpec) []string {
	// Env values are always masked; command args are opaque and may contain secrets.
	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		if out[i] == "--env" && i+1 < len(out) {
			key, _, _ := strings.Cut(out[i+1], "=")
			out[i+1] = key + "=••••••••"
		}
	}
	if len(s.Command) > 0 && len(out) >= len(s.Command) {
		for i := len(out) - len(s.Command); i < len(out); i++ {
			out[i] = "<command argument>"
		}
	}
	return out
}
