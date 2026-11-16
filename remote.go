package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RemoteProfile struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	User           string `json:"user"`
	Port           uint16 `json:"port"`
	Socket         string `json:"socket"`
	IdentityFile   string `json:"identityFile"`
	KnownHostsFile string `json:"knownHostsFile"`
}

var sshHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
var sshUserPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)
var rootlessSocketPattern = regexp.MustCompile(`^/run/user/[1-9][0-9]*/podman/podman\.sock$`)

func validateRemoteProfile(p RemoteProfile) error {
	if err := validateServiceName(p.Name); err != nil {
		return err
	}
	if (!sshHostPattern.MatchString(p.Host) && net.ParseIP(p.Host) == nil) || !sshUserPattern.MatchString(p.User) || p.User == "root" || p.Port == 0 || !rootlessSocketPattern.MatchString(p.Socket) {
		return fmt.Errorf("configure an SSH host, non-root user, port and explicit rootless Podman socket")
	}
	for _, path := range []string{p.IdentityFile, p.KnownHostsFile} {
		if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsFunc(path, func(r rune) bool { return r < 32 || r == 127 })) {
			return fmt.Errorf("SSH file settings must be absolute local paths without control characters; never paste a private key")
		}
	}
	return nil
}
func remoteRoot() (*os.Root, error) { return privateRoot(filepath.Join(getConfigDir(), "remotes")) }
func (p *PodmanService) SaveRemoteProfile(profile RemoteProfile) error {
	if err := validateRemoteProfile(profile); err != nil {
		return err
	}
	root, err := remoteRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return writeRootJSON(root, launcherFile(profile.Name), profile)
}
func loadRemoteProfile(name string) (*RemoteProfile, error) {
	root, err := remoteRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var profile RemoteProfile
	if err := readRootJSON(root, launcherFile(name), &profile); err != nil {
		return nil, err
	}
	if profile.Name != name {
		return nil, fmt.Errorf("profile identity mismatch")
	}
	if err := validateRemoteProfile(profile); err != nil {
		return nil, err
	}
	return &profile, nil
}
func (p *PodmanService) ListRemoteProfiles() ([]RemoteProfile, error) {
	root, err := remoteRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []RemoteProfile{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var profile RemoteProfile
		if err := readRootJSON(root, e.Name(), &profile); err != nil {
			return nil, err
		}
		if err := validateRemoteProfile(profile); err != nil {
			return nil, err
		}
		if launcherFile(profile.Name) != e.Name() {
			return nil, fmt.Errorf("profile filename mismatch")
		}
		out = append(out, profile)
	}
	return out, nil
}

// All remote operations share fixed, non-interactive SSH options. The host-key
// file is a reference to existing operator trust, never copied or rewritten.
func sshConnectionArgs(profile RemoteProfile) []string {
	args := []string{"-F", os.DevNull, "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "BatchMode=yes", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no", "-o", "ConnectTimeout=8", "-p", strconv.Itoa(int(profile.Port))}
	if profile.IdentityFile != "" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", profile.IdentityFile)
	}
	if profile.KnownHostsFile != "" {
		// OpenSSH parses -o values as configuration syntax and expands tokens.
		path := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(profile.KnownHostsFile)
		args = append(args, "-o", `UserKnownHostsFile="`+path+`"`)
	}
	return args
}
func remoteObservationArgs(profile RemoteProfile) []string {
	// SSH sends a remote command through its server shell. Only this fixed
	// command and the strictly validated numeric rootless socket path enter it.
	command := "podman --remote --url unix://" + profile.Socket + " ps --format json -a"
	return append(sshConnectionArgs(profile), "-T", profile.User+"@"+profile.Host, command)
}
func (p *PodmanService) ListRemoteContainers(profileName string) ([]Container, error) {
	profile, err := loadRemoteProfile(profileName)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, _, err := p.runSessionCommand(ctx, "ssh", remoteObservationArgs(*profile)...)
	if err != nil {
		return nil, fmt.Errorf("SSH Podman observation failed; verify pre-provisioned keys, known_hosts and rootless socket (no remote setup is performed)")
	}
	return parseContainersJSON([]byte(out))
}
func remoteAssociation(profile RemoteProfile, c Container) LauncherAssociation {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	profileData := fmt.Sprintf("%s|%s|%s|%d|%s", profile.Name, profile.User, profile.Host, profile.Port, profile.Socket)
	return LauncherAssociation{Key: "remote:" + launcherFile(profileData) + ":" + c.Id, ContainerID: c.Id, ContainerName: name}
}
func (p *PodmanService) remoteContainer(profileName, containerID string) (*RemoteProfile, *Container, error) {
	profile, err := loadRemoteProfile(profileName)
	if err != nil {
		return nil, nil, err
	}
	containers, err := p.ListRemoteContainers(profileName)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range containers {
		if c.Id == containerID {
			return profile, &c, nil
		}
	}
	return nil, nil, fmt.Errorf("remote container no longer exists")
}
func (p *PodmanService) GetRemoteLauncherOptions(profileName, containerID string) (*LauncherOptions, error) {
	profile, c, err := p.remoteContainer(profileName, containerID)
	if err != nil {
		return nil, err
	}
	options := p.launcherOptionsFor(*c, remoteAssociation(*profile, *c))
	options.Message = "Remote host context: web access uses an explicit SSH loopback tunnel. " + options.Message
	return &options, nil
}
func (p *PodmanService) SaveRemoteLauncherDefinition(profileName, containerID string, d LauncherDefinition) error {
	profile, c, err := p.remoteContainer(profileName, containerID)
	if err != nil {
		return err
	}
	if d.Association != remoteAssociation(*profile, *c) {
		return fmt.Errorf("remote association changed; review again")
	}
	if d.Kind != "web" && d.Kind != "gui" && d.Kind != "none" {
		return fmt.Errorf("remote terminal execution is not supported by this viewer")
	}
	if _, err := validateLauncher(d, c.PortMappings); err != nil {
		return err
	}
	return writeLauncher(d)
}

type RemoteTunnel struct {
	ID          string `json:"id"`
	Profile     string `json:"profile"`
	ContainerID string `json:"containerID"`
	URL         string `json:"url"`
	State       string `json:"state"`
	Message     string `json:"message"`
}
type activeRemoteTunnel struct {
	RemoteTunnel
	cancel     context.CancelFunc
	controlDir string
}

var tunnelMu sync.Mutex
var remoteTunnels = map[string]*activeRemoteTunnel{}

func (p *PodmanService) ListRemoteTunnels() []RemoteTunnel {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	out := []RemoteTunnel{}
	for _, t := range remoteTunnels {
		out = append(out, t.RemoteTunnel)
	}
	return out
}
func (p *PodmanService) CloseRemoteTunnel(id string) error {
	tunnelMu.Lock()
	t := remoteTunnels[id]
	if t != nil {
		delete(remoteTunnels, id)
	}
	tunnelMu.Unlock()
	if t == nil {
		return fmt.Errorf("tunnel is not active")
	}
	t.cancel()
	removeTunnelControlDir(t.controlDir)
	return nil
}
func sshTunnelArgs(profile RemoteProfile, local uint16, destination string, remote uint16) []string {
	forward := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(local))) + ":" + net.JoinHostPort(destination, strconv.Itoa(int(remote)))
	args := append(sshConnectionArgs(profile), "-N", "-T", "-o", "ForkAfterAuthentication=no", "-o", "ExitOnForwardFailure=yes", "-L", forward)
	return append(args, profile.User+"@"+profile.Host)
}
func (p *PodmanService) startSSHTunnel(ctx context.Context, args []string) (<-chan error, error) {
	if p.tunnelStart != nil {
		return p.tunnelStart(ctx, "ssh", args...)
	}
	path, err := p.lookPathFn()("ssh")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	output := &boundedCommandOutput{limit: 4096}
	cmd.Stderr = output
	if err := configureTunnelLifetime(cmd); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := cmd.Start()
		started <- err
		if err != nil {
			close(done)
			return
		}
		done <- cmd.Wait()
		close(done)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return done, nil
}
func (p *PodmanService) OpenRemoteWebEndpoint(profileName, containerID, endpointID string, confirmed bool) (*OpenWebResult, error) {
	if !confirmed {
		return nil, fmt.Errorf("approve an SSH tunnel to this remote endpoint before opening")
	}
	profile, c, err := p.remoteContainer(profileName, containerID)
	if err != nil {
		return nil, err
	}
	if c.State != "running" {
		return nil, fmt.Errorf("remote container is stopped")
	}
	root, err := openLauncherRoot()
	if err != nil {
		return nil, err
	}
	d, err := readLauncher(root, remoteAssociation(*profile, *c).Key)
	root.Close()
	if err != nil {
		return nil, err
	}
	if _, err := validateLauncher(*d, c.PortMappings); err != nil {
		return nil, err
	}
	var endpoint *WebEndpoint
	for _, e := range d.Endpoints {
		if e.ID == endpointID {
			copy := e
			endpoint = &copy
		}
	}
	if endpoint == nil {
		return nil, fmt.Errorf("remote endpoint is not configured")
	}
	target, err := launcherURL(*endpoint)
	if err != nil {
		return nil, err
	}
	remoteURL, _ := url.Parse(target)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	tunnelID := newSessionID()
	controlDir := "tunnel-" + tunnelID
	runtimeRoot, err := privateRoot(sessionRuntimeDir())
	if err != nil {
		return nil, err
	}
	if err := runtimeRoot.Mkdir(controlDir, 0700); err != nil {
		runtimeRoot.Close()
		return nil, err
	}
	controlPath := filepath.Join(runtimeRoot.Name(), controlDir, "ssh.sock")
	runtimeRoot.Close()
	ctx, cancel := context.WithCancel(context.Background())
	args := sshTunnelArgs(*profile, port, remoteURL.Hostname(), endpoint.Mapping.HostPort)
	args = append([]string{"-o", "ControlMaster=yes", "-o", "ControlPath=" + controlPath}, args...)
	done, err := p.startSSHTunnel(ctx, args)
	if err != nil {
		cancel()
		removeTunnelControlDir(controlDir)
		return nil, fmt.Errorf("SSH forwarding failed; no browser opened")
	}
	localURL := *remoteURL
	localURL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	tunnel := &activeRemoteTunnel{RemoteTunnel: RemoteTunnel{ID: tunnelID, Profile: profileName, ContainerID: containerID, URL: localURL.String(), State: "opening"}, cancel: cancel, controlDir: controlDir}
	tunnelMu.Lock()
	remoteTunnels[tunnel.ID] = tunnel
	tunnelMu.Unlock()
	go func() {
		err := <-done
		tunnelMu.Lock()
		defer tunnelMu.Unlock()
		if current := remoteTunnels[tunnel.ID]; current != nil {
			current.State = "closed"
			if err != nil {
				current.Message = "SSH forwarding ended; check keys, known_hosts and port availability."
			}
		}
	}()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readyCancel()
	for {
		if p.sshTunnelReady(readyCtx, controlPath, *profile) == nil && p.checkWeb(readyCtx, localURL.String()) == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			p.CloseRemoteTunnel(tunnel.ID)
			return &OpenWebResult{Message: "Remote HTTP(S) endpoint did not become ready; tunnel closed. HTTPS certificates must also be valid for the forwarded endpoint."}, nil
		case <-time.After(150 * time.Millisecond):
		}
	}
	tunnelMu.Lock()
	if t := remoteTunnels[tunnel.ID]; t != nil && t.State == "opening" {
		t.State = "active"
	}
	tunnelMu.Unlock()
	freshProfile, fresh, err := p.remoteContainer(profileName, containerID)
	if err != nil || fresh.State != "running" || remoteAssociation(*freshProfile, *fresh) != d.Association {
		p.CloseRemoteTunnel(tunnel.ID)
		return nil, fmt.Errorf("remote container changed while opening; tunnel closed")
	}
	if _, err := validateLauncher(*d, fresh.PortMappings); err != nil {
		p.CloseRemoteTunnel(tunnel.ID)
		return nil, err
	}
	tunnelMu.Lock()
	currentTunnel := remoteTunnels[tunnel.ID]
	closed := currentTunnel == nil || currentTunnel.State == "closed"
	tunnelMu.Unlock()
	if closed {
		p.CloseRemoteTunnel(tunnel.ID)
		return nil, fmt.Errorf("SSH tunnel exited before browser handoff")
	}
	result := p.openWeb(localURL.String())
	result.Message += " SSH forwarding stays visible in Remote connections; close it there when finished."
	return &result, nil
}
func shutdownLauncherHelpers() {
	sessionBuildMu.Lock()
	for _, cancel := range sessionBuildCancels {
		cancel()
	}
	sessionBuildMu.Unlock()
	tunnelMu.Lock()
	dirs := []string{}
	for _, t := range remoteTunnels {
		t.cancel()
		dirs = append(dirs, t.controlDir)
	}
	remoteTunnels = map[string]*activeRemoteTunnel{}
	tunnelMu.Unlock()
	for _, dir := range dirs {
		removeTunnelControlDir(dir)
	}
}

// An authenticated private SSH control socket proves our forwarding helper has
// established its connection; HTTP on an accidentally occupied local port alone
// must not be treated as successful SSH forwarding.
func (p *PodmanService) sshTunnelReady(ctx context.Context, socket string, profile RemoteProfile) error {
	if p.tunnelReady != nil {
		return p.tunnelReady(ctx, socket, profile)
	}
	_, _, err := p.runSessionCommand(ctx, "ssh", "-F", os.DevNull, "-S", socket, "-O", "check", profile.User+"@"+profile.Host)
	return err
}

func removeTunnelControlDir(dir string) {
	if !strings.HasPrefix(dir, "tunnel-") || !sessionIDPattern.MatchString(strings.TrimPrefix(dir, "tunnel-")) {
		return
	}
	root, err := privateRoot(sessionRuntimeDir())
	if err != nil {
		return
	}
	defer root.Close()
	root.Remove(filepath.Join(dir, "ssh.sock"))
	root.Remove(dir)
}
