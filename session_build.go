package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var githubPathPattern = regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`)
var sessionBuildMu sync.Mutex
var sessionBuildCancels = map[string]context.CancelFunc{}

func sessionBuildActive(id string) bool {
	sessionBuildMu.Lock()
	defer sessionBuildMu.Unlock()
	return sessionBuildCancels[id] != nil
}
func validateGitHubSource(source GitHubSource) error {
	u, err := url.Parse(source.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !githubPathPattern.MatchString(u.Path) || u.RawPath != "" {
		return fmt.Errorf("use a public HTTPS github.com/owner/repository URL without credentials or parameters")
	}
	for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("invalid GitHub repository path")
		}
	}
	if !commitPattern.MatchString(source.Commit) {
		return fmt.Errorf("enter the full immutable 40-character commit SHA")
	}
	for _, path := range []string{source.Subdirectory, source.Containerfile} {
		if path == "" {
			continue
		}
		if filepath.IsAbs(path) || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\\\x00") || strings.HasPrefix(path, ".git") {
			return fmt.Errorf("build paths must be clean relative paths inside the checkout")
		}
	}
	return nil
}
func buildFileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func gitSafeArgs(args ...string) []string {
	return append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=never"}, args...)
}
func redactedBuildLog(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if secretKeyPattern.MatchString(line) {
			lines[i] = "[line containing potentially sensitive material omitted]"
		}
	}
	out := strings.Join(lines, "\n")
	if len(out) > 128*1024 {
		out = out[:128*1024] + "\n[truncated]"
	}
	return out
}
func (p *PodmanService) ReviewGitHubSession(spec DisposableSessionSpec) (*SessionRuntimeState, error) {
	if spec.GitHub == nil {
		return nil, fmt.Errorf("GitHub source is missing")
	}
	if err := validateGitHubSource(*spec.GitHub); err != nil {
		return nil, err
	}
	check := spec
	check.Image = "review-pending"
	check.RequiredEnv = nil
	check.RequireCommand = false
	if err := validateSessionSpec(check); err != nil {
		return nil, err
	}
	state := SessionRuntimeState{SchemaVersion: sessionSchema, ID: newSessionID(), Token: newSessionID(), State: "draft", Spec: savedSessionSpec(spec), StartedAt: time.Now().UTC().Format(time.RFC3339)}
	operation, err := acquireSessionOperation(state.ID)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	state.RuntimeIdentity, err = p.sessionRuntimeIdentity()
	if err != nil {
		return nil, err
	}
	state.ContainerName = spec.Name + "-" + state.ID[:8]
	if err := saveSessionState(state); err != nil {
		return nil, err
	}
	// Make only a private checkout workspace; do not create retained mounts yet.
	work := state
	work.Spec.Config = SessionMount{}
	work.Spec.Data = SessionMount{}
	work.Spec.ExternalBinds = nil
	workspaceBinds, err := prepareSessionWorkspace(&work)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	closeSessionBindSources(workspaceBinds)
	ctx, cancel := context.WithTimeout(sessionCommandContext(context.Background(), operation), 2*time.Minute)
	defer cancel()
	repo := filepath.Join(sessionRuntimeDir(), state.ID, "repo")
	commands := [][]string{
		gitSafeArgs("clone", "--no-checkout", "--filter=blob:none", "--", spec.GitHub.URL, repo),
		gitSafeArgs("-C", repo, "fetch", "--depth=1", "origin", strings.ToLower(spec.GitHub.Commit)),
		gitSafeArgs("-C", repo, "checkout", "--detach", strings.ToLower(spec.GitHub.Commit)),
	}
	for _, args := range commands {
		out, errs, err := p.runSessionCommand(ctx, "git", args...)
		if err != nil {
			state.BuildLog = redactedBuildLog(out + "\n" + errs)
			return p.sessionLaunchFailed(state, fmt.Errorf("public repository checkout failed (private/auth-required repositories are unsupported); review log and cleanup"))
		}
	}
	out, _, err := p.runSessionCommand(ctx, "git", gitSafeArgs("-C", repo, "rev-parse", "HEAD")...)
	if err != nil || !strings.EqualFold(strings.TrimSpace(out), spec.GitHub.Commit) {
		return p.sessionLaunchFailed(state, fmt.Errorf("checkout commit identity could not be verified"))
	}
	state.Spec.GitHub.Commit = strings.ToLower(strings.TrimSpace(out))
	contextPath, err := reviewedBuildContext(state.ID, spec.GitHub.Subdirectory)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	if info, err := os.Stat(contextPath); err != nil || !info.IsDir() {
		return p.sessionLaunchFailed(state, fmt.Errorf("build subdirectory is unavailable"))
	}
	names := []string{spec.GitHub.Containerfile}
	if names[0] == "" {
		names = []string{"Containerfile", "Dockerfile"}
	}
	root, err := os.OpenRoot(contextPath)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	defer root.Close()
	var data []byte
	selected := ""
	for _, name := range names {
		if _, err := resolveWithinRoot(contextPath, name); err != nil {
			continue
		}
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024 {
			continue
		}
		data, err = root.ReadFile(name)
		if err == nil {
			selected = name
			break
		}
	}
	if selected == "" {
		return p.sessionLaunchFailed(state, fmt.Errorf("no readable Containerfile or Dockerfile exists in the selected build context; source inference and setup scripts are unsupported"))
	}
	state.Spec.GitHub.Containerfile = selected
	state.BuildDefinition = string(data)
	state.BuildDigest = buildFileDigest(data)
	state.Message = "Review this immutable source and build definition before executing repository code. Rootless build, no privileged mounts; Denied RUN networking is currently unsupported: builds require separate network approval. Approved RUN instructions may select host networking. Image pulls, remote ADD and other builder fetches are separate network channels. Base layers and caches may remain."
	if err := saveSessionState(state); err != nil {
		return nil, err
	}
	return &state, nil
}
func (p *PodmanService) StartReviewedSessionBuild(id string, confirmed bool, allowBuildNetwork bool) (*SessionRuntimeState, error) {
	if err := p.ensureLocalContext(true); err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, fmt.Errorf("review source, commit and build definition; explicit build approval is required")
	}
	// Podman's build-wide --network=none is overridden by RUN --network=host.
	// No supported builder boundary currently enforces denied RUN networking.
	// Refuse BEFORE starting any builder; never silently upgrade consent.
	if !allowBuildNetwork {
		return nil, fmt.Errorf("denied RUN networking is unsupported by the current builder integration: --network=none can be overridden by repository instructions; no build was started. Separate explicit network approval is required (RUN may use host networking). Image pulls, remote ADD and other builder fetch channels are separate")
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			operation.Close()
		}
	}()
	state, err := loadSessionState(id)
	if err != nil {
		return nil, err
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return nil, err
	}
	if state.Spec.GitHub == nil || state.BuildDigest == "" || state.State != "draft" && state.State != "build_failed" && state.State != "build_cancelled" {
		return nil, fmt.Errorf("source must be reviewed before building")
	}
	if err := validateGitHubSource(*state.Spec.GitHub); err != nil {
		return nil, err
	}
	contextPath, err := reviewedBuildContext(state.ID, state.Spec.GitHub.Subdirectory)
	if err != nil {
		return nil, err
	}
	verificationCtx, verifyCancel := context.WithTimeout(sessionCommandContext(context.Background(), operation), 20*time.Second)
	defer verifyCancel()
	repo := filepath.Join(sessionRuntimeDir(), id, "repo")
	head, _, headErr := p.runSessionCommand(verificationCtx, "git", gitSafeArgs("-C", repo, "rev-parse", "HEAD")...)
	status, _, statusErr := p.runSessionCommand(verificationCtx, "git", gitSafeArgs("-C", repo, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")...)
	if headErr != nil || statusErr != nil || !strings.EqualFold(strings.TrimSpace(head), state.Spec.GitHub.Commit) || strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("checkout changed or could not be verified; review immutable source again")
	}
	root, err := os.OpenRoot(contextPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	definitionPath, err := containedBuildDefinition(contextPath, state.Spec.GitHub.Containerfile)
	if err != nil {
		return nil, fmt.Errorf("build definition must remain inside selected context")
	}
	data, err := root.ReadFile(state.Spec.GitHub.Containerfile)
	if err != nil || buildFileDigest(data) != state.BuildDigest {
		return nil, fmt.Errorf("build definition changed; review the source again")
	}
	if sessionBuildActive(id) {
		return nil, fmt.Errorf("build is already running")
	}
	ctx, cancel := context.WithTimeout(sessionCommandContext(context.Background(), operation), 20*time.Minute)
	sessionBuildMu.Lock()
	sessionBuildCancels[id] = cancel
	sessionBuildMu.Unlock()
	state.State = "building"
	state.BuildOperation = newSessionID()
	state.InterruptedBuild = false
	state.BuildNetwork = allowBuildNetwork
	state.ImageTag = "localhost/podder-session:" + id
	state.BuildLog = ""
	if err := saveSessionState(*state); err != nil {
		cancel()
		sessionBuildMu.Lock()
		delete(sessionBuildCancels, id)
		sessionBuildMu.Unlock()
		return nil, err
	}
	copy := *state
	transferred = true
	go func() {
		defer operation.Close()
		defer cancel()
		defer func() { sessionBuildMu.Lock(); delete(sessionBuildCancels, id); sessionBuildMu.Unlock() }()
		network := "none"
		if allowBuildNetwork {
			network = "private"
		}
		// A unique tag is checked before the build; a preexisting tag is never owned.
		if _, err := p.resolveImageID(copy.ImageTag); err == nil {
			copy.State = "build_failed"
			copy.Message = "Unique session tag already exists; it is not owned and will not be removed."
		} else {
			existingImages, listErr := p.ListImages()
			preexisting := map[string]bool{}
			for _, image := range existingImages {
				preexisting[canonicalImageIdentity(image.Id)] = true
			}
			args := []string{"build", "--network", network, "--tag", copy.ImageTag, "--label", "io.podder.session-id=" + copy.ID, "--file", definitionPath, contextPath}
			// Revalidate containment and context immediately before execution.
			checkedContext, contextErr := reviewedBuildContext(copy.ID, copy.Spec.GitHub.Subdirectory)
			checkedDefinition, definitionErr := containedBuildDefinition(contextPath, copy.Spec.GitHub.Containerfile)
			head, _, headErr := p.runSessionCommand(ctx, "git", gitSafeArgs("-C", repo, "rev-parse", "HEAD")...)
			status, _, statusErr := p.runSessionCommand(ctx, "git", gitSafeArgs("-C", repo, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")...)
			currentData, readErr := os.ReadFile(definitionPath)
			unchanged := headErr == nil && statusErr == nil && strings.EqualFold(strings.TrimSpace(head), copy.Spec.GitHub.Commit) && strings.TrimSpace(status) == "" && readErr == nil && buildFileDigest(currentData) == copy.BuildDigest
			runtimeErr := p.verifySessionRuntime(&copy)
			var out, errs string
			var buildErr error
			if !unchanged || contextErr != nil || definitionErr != nil || checkedContext != contextPath || checkedDefinition != definitionPath || runtimeErr != nil {
				buildErr = fmt.Errorf("reviewed build context or runtime changed")
			} else {
				out, errs, buildErr = p.runSessionCommand(ctx, "podman", args...)
			}
			copy.BuildLog = redactedBuildLog(out + "\n" + errs)
			if buildErr != nil {
				copy.State = "build_failed"
				copy.Message = "Build failed. Review logs; retry or explicitly clean up the checkout."
				if ctx.Err() != nil {
					copy.State = "build_cancelled"
					copy.Message = "Build command cancelled. Build caches may remain; review cleanup."
				}
			} else {
				imageID, err := p.resolveImageID(copy.ImageTag)
				if err != nil {
					copy.State = "build_failed"
					copy.Message = "Build completed but immutable image identity could not be verified; image retained."
				} else {
					copy.ImageID = imageID
					copy.ImageOwned = listErr == nil && !preexisting[canonicalImageIdentity(imageID)] && p.sessionImageHasLabel(copy.ImageTag, copy.ID)
					copy.Spec.Image = imageID
					copy.State = "built"
					copy.Message = "Build complete. Review launch preview before starting the session."
				}
			}
		}
		if !copy.ImageOwned {
			copy.Retained = append(copy.Retained, "Build tag/image may remain and is not eligible for automatic deletion: "+copy.ImageTag)
		}
		sessionOperationMu.Lock()
		defer sessionOperationMu.Unlock()
		if err := completeSessionBuild(copy); err != nil { /* Initial journal retains the unique tag for manual recovery. */
		}
	}()
	return state, nil
}
func (p *PodmanService) CancelSessionBuild(id string) error {
	sessionBuildMu.Lock()
	cancel := sessionBuildCancels[id]
	sessionBuildMu.Unlock()
	if cancel == nil {
		return fmt.Errorf("no active build")
	}
	cancel()
	return nil
}
func (p *PodmanService) LaunchBuiltSession(id string, env map[string]string, command []string) (*SessionRuntimeState, error) {
	return p.LaunchBuiltSessionWithConsent(id, env, command, SessionLaunchConsent{})
}
func (p *PodmanService) LaunchBuiltSessionWithConsent(id string, env map[string]string, command []string, consent SessionLaunchConsent) (*SessionRuntimeState, error) {
	if err := p.ensureLocalContext(true); err != nil {
		return nil, err
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	if sessionBuildActive(id) {
		return nil, fmt.Errorf("wait for build completion")
	}
	state, err := loadSessionState(id)
	if err != nil {
		return nil, err
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return nil, err
	}
	if state.State != "built" || state.ImageID == "" {
		return nil, fmt.Errorf("a successful reviewed build is required")
	}
	spec := state.Spec
	spec.AllowExternalBinds = consent.AllowExternalBinds
	spec.AllowNetworkExposure = consent.AllowNetworkExposure
	spec.ExternalBindApprovals = consent.ExternalBindApprovals
	spec.Env = env
	if len(command) > 0 {
		spec.Command = command
	}
	if err := validateSessionSpec(spec); err != nil {
		return nil, err
	}
	image, err := p.resolveImageID(state.ImageTag)
	if err != nil || !sameImageIdentity(image, state.ImageID) {
		return nil, fmt.Errorf("built image identity changed")
	}
	if err := p.validateMappingsForCreate(spec.PortMappings, true); err != nil {
		return nil, err
	}
	return p.launchSessionState(sessionCommandContext(context.Background(), operation), spec, state)
}

func (p *PodmanService) sessionImageHasLabel(tag, id string) bool {
	out, _, err := p.runCommand("image", "inspect", "--format", "json", tag)
	if err != nil {
		return false
	}
	var images []struct {
		Labels map[string]string `json:"Labels"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if json.Unmarshal([]byte(out), &images) != nil || len(images) != 1 {
		return false
	}
	return images[0].Labels["io.podder.session-id"] == id || images[0].Config.Labels["io.podder.session-id"] == id
}

// The checkout itself cannot be a symlink to session-generated material. Context
// symlinks may resolve only within that checkout, and definitions within context.
func reviewedBuildContext(id, subdirectory string) (string, error) {
	repo := filepath.Join(sessionRuntimeDir(), id, "repo")
	info, err := os.Lstat(repo)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("reviewed checkout is unavailable or redirected")
	}
	path, err := resolveWithinRoot(repo, subdirectory)
	if err != nil {
		return "", fmt.Errorf("build context must remain inside reviewed checkout")
	}
	info, err = os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("build context must be a checkout directory")
	}
	return filepath.EvalSymlinks(path)
}

func containedBuildDefinition(contextPath, name string) (string, error) {
	path, err := resolveWithinRoot(contextPath, name)
	if err != nil {
		return "", fmt.Errorf("build definition must remain inside selected context")
	}
	return filepath.EvalSymlinks(path)
}
