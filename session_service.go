package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var sessionOperationMu sync.Mutex

func (p *PodmanService) PreviewDisposableSession(spec DisposableSessionSpec) (*SessionPreview, error) {
	if err := validateSessionSpec(spec); err != nil {
		return nil, err
	}
	if !descriptorMountHandoffAvailable() && (len(spec.ExternalBinds) != 0 || spec.Config.Target != "" || spec.Data.Target != "") {
		return nil, fmt.Errorf("session mounts require descriptor-backed mount handoff on this platform")
	}
	// Reserve an opaque identity in the preview; launch refuses any existing journal.
	if spec.LaunchID == "" {
		spec.LaunchID = newSessionID()
	}
	if err := approveExternalSessionBinds(&spec); err != nil {
		return nil, err
	}
	binds := plannedSessionBinds(spec, spec.LaunchID)
	args, err := buildSessionRunArgs(spec, spec.LaunchID, binds, false)
	if err != nil {
		return nil, err
	}
	summary := []string{"Container auto-removes on exit. Images, base layers and build caches are retained unless explicitly owned and selected for cleanup.", "GUI exit leaves the detached container running.", "Environment values are not saved; recipes require re-entry."}
	if spec.KeepID {
		summary = append(summary, "Map the current user's UID/GID into the container (keep-id) for explicitly selected writable/config mounts; image user behavior can change.")
	}
	if spec.Preset == "olivetin-hello" {
		summary = append(summary, "Starter writes config.yaml only if absent: one Say hello action running printf inside the session. Existing configuration is never overwritten.")
	}
	switch spec.Policy {
	case "temporary":
		summary = append(summary, "No reusable recipe. Explicit config/data mounts use temporary owned directories.")
	case "configuration":
		summary = append(summary, "Keep recipe and explicit config mount; explicit data mount is temporary.")
	case "data":
		summary = append(summary, "Keep recipe and explicitly designated config/data mounts.")
	}
	for _, b := range spec.ExternalBinds {
		summary = append(summary, "External directory retained: "+b.HostPath)
	}
	if spec.Config.Target != "" {
		summary = append(summary, "Config mount target: "+spec.Config.Target)
	}
	if spec.Data.Target != "" {
		summary = append(summary, "Data mount target: "+spec.Data.Target)
	}
	if len(spec.ExternalBinds) > 0 {
		summary = append(summary, "External mount arguments will be validated before launch; external contents are never deleted.")
	}
	previewSpec := savedSessionSpec(spec)
	previewSpec.LaunchID = spec.LaunchID
	previewSpec.ExternalBinds = append([]BindMountSpec{}, spec.ExternalBinds...)
	previewSpec.ExternalBindApprovals = append([]SessionBindApproval{}, spec.ExternalBindApprovals...)
	return &SessionPreview{Argv: maskedSessionArgs(args, spec), Summary: summary, Spec: previewSpec}, nil
}
func (p *PodmanService) LaunchDisposableSession(spec DisposableSessionSpec) (*SessionRuntimeState, error) {
	if err := p.ensureLocalContext(true); err != nil {
		return nil, err
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	if err := validateSessionSpec(spec); err != nil {
		return nil, err
	}
	if err := verifyExternalSessionBindApprovals(&spec); err != nil {
		return nil, err
	}
	runtimeID, err := p.sessionRuntimeIdentity()
	if err != nil {
		return nil, err
	}
	if spec.GitHub != nil && !regexp.MustCompile(`^(sha256:)?[0-9a-f]{64}$`).MatchString(spec.Image) {
		return nil, fmt.Errorf("review and build GitHub sources through the build flow first")
	}
	// Check before any persistent state is created; repeat immediately before run.
	if err := p.validateMappingsForCreate(spec.PortMappings, true); err != nil {
		return nil, err
	}
	image, err := p.resolveImageID(spec.Image)
	if err != nil {
		if err := p.PullImage(spec.Image); err != nil {
			return nil, err
		}
		image, err = p.resolveImageID(spec.Image)
		if err != nil {
			return nil, err
		}
	}
	spec.Image = image
	id := spec.LaunchID
	if id == "" {
		id = newSessionID()
	}
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	if _, err := loadSessionState(id); err == nil || !os.IsNotExist(err) {
		return nil, fmt.Errorf("launch identity already exists or cannot be verified; generate a new preview")
	}
	state := SessionRuntimeState{RuntimeIdentity: runtimeID, SchemaVersion: sessionSchema, ID: id, Token: newSessionID(), State: "starting", ImageID: image, Spec: savedSessionSpec(spec), StartedAt: time.Now().UTC().Format(time.RFC3339), Retained: []string{}}
	return p.launchSessionState(sessionCommandContext(context.Background(), operation), spec, &state)
}

// Shared launch transaction for registry images and reviewed builds.
func (p *PodmanService) launchSessionState(ctx context.Context, spec DisposableSessionSpec, statePtr *SessionRuntimeState) (*SessionRuntimeState, error) {
	state := *statePtr
	if err := p.verifySessionRuntime(&state); err != nil {
		return nil, err
	}
	state.Spec = savedSessionSpec(spec)
	state.State = "starting"
	state.ContainerName = spec.Name + "-" + state.ID[:8]
	if err := saveSessionState(state); err != nil {
		return nil, err
	}
	binds, err := prepareSessionWorkspace(&state)
	if err != nil {
		state.State = "cleanup_pending"
		state.Message = err.Error()
		_ = saveSessionState(state)
		return &state, nil
	}
	defer closeSessionBindSources(binds)
	if err := verifyExternalSessionBindApprovals(&spec); err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	state.Spec = savedSessionSpec(spec)
	// The journal is durable before a container can begin using any mount.
	if err := saveSessionState(state); err != nil {
		return nil, err
	}
	pinnedBinds, closePinned, err := pinSessionBinds(spec.ExternalBinds, spec.ExternalBindApprovals, binds)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	defer closePinned()
	if p.sessionMountsPinnedHook != nil {
		p.sessionMountsPinnedHook()
	}
	executionBinds, mountPlan, err := p.preparePinnedMountPlan(state.ID, pinnedBinds)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	defer cleanupPinnedMountPlan(mountPlan)
	runSpec := spec
	runSpec.ExternalBinds = nil
	runSpec.ExternalBindApprovals = nil
	args, err := BuildSessionRunArgs(runSpec, state.ID, executionBinds)
	if err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	if err := p.validateMappingsForCreate(spec.PortMappings, true); err != nil {
		return p.sessionLaunchFailed(state, err)
	}
	if err := p.verifySessionRuntime(&state); err != nil {
		return nil, err
	}
	// Persist uncertainty before starting any command that may outlive Podder.
	state.LaunchOperation = newSessionID()
	if err := saveSessionState(state); err != nil {
		return nil, err
	}
	stdout, _, runErr := p.runSessionWithPinnedEnvironment(ctx, spec.Env, mountPlan, args...)
	state.ContainerID = strings.TrimSpace(stdout)
	if runErr != nil {
		state.State = "cleanup_pending"
		state.InterruptedLaunch = true
		state.Message = "Container launch failed; verify all launch processes have exited before acknowledging cleanup (arguments and environment omitted)."
		if err := completeSessionLaunch(state); err != nil {
			return nil, err
		}
		state.LaunchOperation = ""
		return &state, nil
	}
	// Keep the durable starting receipt until the live result is verified.
	// A successful command alone does not prove a container's final identity.
	if err := p.verifySessionContainer(&state); err != nil {
		state.State = "cleanup_pending"
		state.InterruptedLaunch = true
		state.Message = err.Error()
		if e := completeSessionLaunch(state); e != nil {
			return nil, e
		}
		state.LaunchOperation = ""
		return &state, nil
	}
	state.State = "running"
	if err := completeSessionLaunch(state); err != nil {
		return nil, fmt.Errorf("container may be running; journal recovery is required: %w", err)
	}
	state.LaunchOperation = ""
	if spec.Policy != "temporary" {
		if err := saveSessionRecipe(spec); err != nil {
			state.Message = "Running, but recipe could not be saved: " + err.Error()
			_ = saveSessionState(state)
		}
	}
	if spec.Web != nil {
		// Settings are explicit; no automatic browser opening on launch.
		d := LauncherDefinition{SchemaVersion: 1, Kind: "web", Association: LauncherAssociation{Key: "container:" + state.ContainerID, ContainerID: state.ContainerID, ContainerName: state.ContainerName}, Endpoints: []WebEndpoint{*spec.Web}}
		if err := p.SaveLauncherDefinition(state.ContainerID, d); err != nil {
			state.Message = "Running; configure application opening manually: " + err.Error()
			_ = saveSessionState(state)
		}
	}
	return &state, nil
}
func (p *PodmanService) sessionLaunchFailed(state SessionRuntimeState, err error) (*SessionRuntimeState, error) {
	state.State = "cleanup_pending"
	state.Message = err.Error()
	if e := saveSessionState(state); e != nil {
		return nil, e
	}
	return &state, nil
}
func sessionContainerMatches(state SessionRuntimeState, c Container) bool {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	return c.AutoRemove && c.Labels["io.podder.session"] == "true" && c.Labels["io.podder.session-id"] == state.ID && c.Labels["io.podder.managed"] != "true" && c.Provenance.Type == "adhoc" && name == state.ContainerName && sameImageIdentity(c.ImageID, state.ImageID)
}
func (p *PodmanService) sessionContainer(state *SessionRuntimeState) (*Container, error) {
	if err := p.verifySessionRuntime(state); err != nil {
		return nil, err
	}
	live, err := p.ListContainers(true)
	if err != nil {
		return nil, err
	}
	var match *Container
	for _, c := range live {
		if state.ContainerID != "" && c.Id == state.ContainerID || c.Labels["io.podder.session-id"] == state.ID {
			if !sessionContainerMatches(*state, c) {
				return nil, fmt.Errorf("session container identity/labels/image/ownership mismatch; automatic mutation blocked")
			}
			if state.ContainerID != "" && c.Id != state.ContainerID {
				return nil, fmt.Errorf("session ID has another container; automatic mutation blocked")
			}
			if match != nil {
				return nil, fmt.Errorf("duplicate session identity; automatic mutation blocked")
			}
			copy := c
			match = &copy
		}
	}
	return match, nil
}
func (p *PodmanService) verifySessionContainer(state *SessionRuntimeState) error {
	c, err := p.sessionContainer(state)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("container exited or auto-removed before verification; cleanup remains pending")
	}
	if c.State != "running" {
		return fmt.Errorf("container is not running")
	}
	state.ContainerID = c.Id
	return nil
}
func (p *PodmanService) ListDisposableSessions() ([]SessionRuntimeState, error) {
	if err := p.ensureLocalContext(true); err != nil {
		return nil, err
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	states, err := listSessionStates()
	if err != nil {
		return nil, err
	}
	for i := range states {
		s := &states[i]
		operation, err := acquireSessionOperation(s.ID)
		if err != nil {
			s.Message = "Session operation is active in another worker/process; cleanup is blocked."
			continue
		}
		func() {
			defer operation.Close()
			current, err := loadSessionState(s.ID)
			if err != nil {
				s.Message = "Journal changed; refresh sessions."
				return
			}
			*s = *current
			if err := p.verifySessionRuntime(s); err != nil {
				s.Message = err.Error()
				return
			}
			if interruptedSessionBuild(s) {
				s.State = "cleanup_pending"
				s.InterruptedBuild = true
				s.ImageOwned = false
				s.Message = "Interrupted build: independently verify all builders have exited before acknowledging cleanup. Images and caches are retained."
			}
			if interruptedSessionLaunch(s) {
				s.InterruptedLaunch = true
				s.State = "cleanup_pending"
				s.Message = "Interrupted launch: independently verify all launch commands have exited before acknowledging cleanup."
			}
			if s.State == "stopped" {
				return
			}
			c, err := p.sessionContainer(s)
			if err != nil {
				s.Message = err.Error()
				return
			}
			if c != nil {
				s.ContainerID = c.Id
				if c.State == "running" {
					s.State = "running"
				} else {
					s.State = "stopped_container"
				}
			} else if s.State != "building" && s.State != "draft" && s.State != "built" && s.State != "build_failed" && s.State != "build_cancelled" && !s.InterruptedBuild && !s.InterruptedLaunch {
				s.State = "cleanup_pending"
				s.Message = "Container is absent; review retained resources and cleanup."
			}
			if err := saveSessionState(*s); err != nil {
				s.Message = "Could not persist reconciliation; automatic cleanup requires fresh checks."
			}
		}()
	}
	known := map[string]bool{}
	for _, s := range states {
		known[s.ID] = true
	}
	live, err := p.ListContainers(true)
	if err != nil {
		return nil, err
	}
	for _, c := range live {
		id := c.Labels["io.podder.session-id"]
		if c.Labels["io.podder.session"] == "true" && !known[id] {
			name := id
			if len(c.Names) > 0 {
				name = c.Names[0]
			}
			states = append(states, SessionRuntimeState{ID: id, State: "untracked", ContainerID: c.Id, Spec: DisposableSessionSpec{Name: name}, Message: "Session labels exist without an ownership journal. Observation only; automatic cleanup is blocked."})
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].StartedAt > states[j].StartedAt })
	return states, nil
}

type SessionCleanupPreview struct {
	ID                     string   `json:"id"`
	ContainerID            string   `json:"containerID"`
	TemporaryDirectory     string   `json:"temporaryDirectory"`
	Retained               []string `json:"retained"`
	OwnedImage             string   `json:"ownedImage"`
	NeedLaunchConfirmation bool     `json:"needLaunchConfirmation"`
	NeedBuildConfirmation  bool     `json:"needBuildConfirmation"`
	Warning                string   `json:"warning"`
}

func (p *PodmanService) PreviewSessionCleanup(id string) (*SessionCleanupPreview, error) {
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	state, err := loadSessionState(id)
	if err != nil {
		return nil, err
	}
	if sessionBuildActive(id) {
		return nil, fmt.Errorf("cancel the active build and wait for it to finish before cleanup")
	}
	c, err := p.sessionContainer(state)
	if err != nil {
		return nil, err
	}
	result := &SessionCleanupPreview{ID: id, NeedBuildConfirmation: interruptedSessionBuild(state), NeedLaunchConfirmation: interruptedSessionLaunch(state), TemporaryDirectory: sessionRuntimeDir() + string(os.PathSeparator) + id, Retained: state.Retained, Warning: "Stop only this verified session, then remove its owned temporary workspace. Retained data, external directories, preexisting images and caches are untouched."}
	if c != nil {
		result.ContainerID = c.Id
	}
	if state.ImageOwned {
		result.OwnedImage = state.ImageID
	}
	return result, nil
}

// Explicit confirmation never replaces the live identity and ownership checks.
// The UI previews resources before sending this destructive action.
func (p *PodmanService) StopAndCleanupSession(id string, confirmed bool, removeOwnedImage bool) (*SessionRuntimeState, error) {
	if err := p.ensureLocalContext(true); err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, fmt.Errorf("review cleanup preview and confirm before stopping/deleting session resources")
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	state, err := loadSessionState(id)
	if err != nil {
		return nil, err
	}
	if sessionBuildActive(id) {
		return nil, fmt.Errorf("cancel the active build and wait for it to finish before cleanup")
	}
	if interruptedSessionBuild(state) {
		return nil, fmt.Errorf("verify no builder is using this workspace, then explicitly acknowledge the interrupted build before cleanup")
	}
	if interruptedSessionLaunch(state) {
		return nil, fmt.Errorf("verify no launch command is using this workspace, then explicitly acknowledge the interrupted launch before cleanup")
	}
	c, err := p.sessionContainer(state)
	if err != nil {
		return nil, err
	}
	if c != nil {
		state.ContainerID = c.Id
		state.State = "stopping"
		if err := saveSessionState(*state); err != nil {
			return nil, err
		}
		if err := p.verifySessionRuntime(state); err != nil {
			return nil, err
		}
		if _, _, err := p.runCommand("stop", "--time", "10", c.Id); err != nil {
			return p.sessionCleanupFailed(*state, fmt.Errorf("graceful stop failed; no artifacts were deleted"))
		}
		absent := false
		for i := 0; i < 8; i++ {
			current, e := p.sessionContainer(state)
			if e != nil {
				return p.sessionCleanupFailed(*state, e)
			}
			if current == nil {
				absent = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !absent {
			return p.sessionCleanupFailed(*state, fmt.Errorf("container remains present; no artifacts were deleted (auto-removal not yet confirmed)"))
		}
	}
	state.State = "cleanup_pending"
	if err := saveSessionState(*state); err != nil {
		return nil, err
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return nil, err
	}
	if err := p.removeSessionWorkspace(*state); err != nil {
		return p.sessionCleanupFailed(*state, err)
	}
	if removeOwnedImage && state.ImageOwned {
		if err := p.cleanupSessionImage(state); err != nil {
			return p.sessionCleanupFailed(*state, err)
		}
	}
	if state.ContainerID != "" {
		root, err := openLauncherRoot()
		if err != nil {
			return p.sessionCleanupFailed(*state, err)
		}
		key := "container:" + state.ContainerID
		if d, err := readLauncher(root, key); err == nil && d.Association.ContainerID == state.ContainerID {
			if err := root.Remove(launcherFile(key)); err != nil {
				root.Close()
				return p.sessionCleanupFailed(*state, err)
			}
		}
		root.Close()
	}
	state.BuildLog = ""
	state.BuildDefinition = ""
	state.State = "stopped"
	state.Message = "Container absent. Owned temporary workspace removed. Retained resources and caches remain."
	if state.Spec.Policy == "temporary" {
		if !state.ImageOwned {
			root, err := manifestRoot()
			if err != nil {
				return nil, err
			}
			defer root.Close()
			if err := root.Remove(state.ID + ".json"); err != nil {
				return nil, err
			}
			return state, nil
		}
		// Keep only the unremoved image ownership receipt, never a temporary recipe/log.
		state.Spec = DisposableSessionSpec{SchemaVersion: sessionSchema, Name: state.Spec.Name, Policy: "temporary"}
	}
	if err := saveSessionState(*state); err != nil {
		return nil, err
	}
	return state, nil
}
func (p *PodmanService) sessionCleanupFailed(state SessionRuntimeState, err error) (*SessionRuntimeState, error) {
	state.State = "cleanup_failed"
	state.Message = err.Error()
	if e := saveSessionState(state); e != nil {
		return nil, e
	}
	return &state, nil
}
func (p *PodmanService) cleanupSessionImage(state *SessionRuntimeState) error {
	if err := p.verifySessionRuntime(state); err != nil {
		return err
	}
	if !state.ImageOwned || state.ImageID == "" || state.ImageTag != "localhost/podder-session:"+state.ID {
		return fmt.Errorf("image ownership is not proven")
	}
	exactID, err := exactFullImageID(state.ImageID)
	if err != nil {
		return fmt.Errorf("image ownership is not proven: %w", err)
	}
	// Resolve only the unique tag we created; never delete a broad/shared image ID.
	id, err := p.resolveImageID(state.ImageTag)
	if err != nil {
		return fmt.Errorf("owned image could not be verified; retained")
	}
	if !sameImageIdentity(id, exactID) || !p.sessionImageHasLabel(exactID, state.ID) {
		return fmt.Errorf("session image tag changed; retained")
	}
	containers, err := p.ListContainers(true)
	if err != nil {
		return err
	}
	for _, c := range containers {
		if sameImageIdentity(c.ImageID, exactID) {
			return fmt.Errorf("session image is used by another container; retained")
		}
	}
	images, err := p.ListImages()
	if err != nil {
		return err
	}
	foundImage := false
	for _, image := range images {
		if sameImageIdentity(image.Id, exactID) {
			foundImage = true
			for _, name := range image.Names {
				if name != state.ImageTag {
					return fmt.Errorf("session image has another tag; retained")
				}
			}
		}
	}
	if !foundImage {
		return fmt.Errorf("image inventory did not verify the owned image; retained")
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return err
	}
	if p.imageCleanupVerifiedHook != nil {
		p.imageCleanupVerifiedHook()
	}
	// Recheck tag ownership, immutable-image labels, users and alternate tags
	// after the deterministic validation barrier. Ambiguity retains everything.
	id, err = p.resolveImageID(state.ImageTag)
	if err != nil || !sameImageIdentity(id, exactID) || !p.sessionImageHasLabel(exactID, state.ID) {
		return fmt.Errorf("session image changed before deletion; retained")
	}
	containers, err = p.ListContainers(true)
	if err != nil {
		return err
	}
	for _, c := range containers {
		if sameImageIdentity(c.ImageID, exactID) {
			return fmt.Errorf("session image is used by another container; retained")
		}
	}
	images, err = p.ListImages()
	if err != nil {
		return err
	}
	foundImage = false
	for _, image := range images {
		if !sameImageIdentity(image.Id, exactID) {
			continue
		}
		foundImage = true
		for _, name := range image.Names {
			if name != state.ImageTag {
				return fmt.Errorf("session image has another tag; retained")
			}
		}
	}
	if !foundImage {
		return fmt.Errorf("image inventory did not verify the owned image; retained")
	}
	if _, _, err := p.runCommand("image", "rm", "--", exactID); err != nil {
		return fmt.Errorf("owned image removal failed; retry cleanup")
	}
	state.ImageOwned = false
	return nil
}
func (p *PodmanService) SaveSessionRecipe(spec DisposableSessionSpec) error {
	check := spec
	check.RequiredEnv = nil
	check.RequireCommand = false
	if err := validateSessionSpec(check); err != nil {
		return err
	}
	if spec.Policy == "temporary" {
		return fmt.Errorf("choose a retained policy before saving a reusable recipe")
	}
	return saveSessionRecipe(spec)
}
func (p *PodmanService) ExportSessionRecipe(name string) (string, error) {
	recipes, err := p.ListSessionRecipes()
	if err != nil {
		return "", err
	}
	for _, r := range recipes {
		if r.Name == name {
			return exportRecipe(r)
		}
	}
	return "", fmt.Errorf("recipe not found")
}

func (p *PodmanService) AcknowledgeInterruptedSessionBuild(id string, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("confirm you verified no builder is using the session workspace")
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return err
	}
	defer operation.Close()
	if sessionBuildActive(id) {
		return fmt.Errorf("build is still active")
	}
	state, err := loadSessionState(id)
	if err != nil {
		return err
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return err
	}
	if !interruptedSessionBuild(state) {
		return fmt.Errorf("no interrupted build requires acknowledgment")
	}
	state.InterruptedBuild = false
	state.State = "cleanup_pending"
	state.ImageOwned = false
	state.Message = "Interrupted build acknowledged; images/caches retained because complete ownership could not be verified."
	return saveSessionState(*state)
}

// This acknowledgment never overrides an inherited lock or runtime identity.
func (p *PodmanService) AcknowledgeInterruptedSessionLaunch(id string, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("confirm you verified all launch commands have exited")
	}
	sessionOperationMu.Lock()
	defer sessionOperationMu.Unlock()
	operation, err := acquireSessionOperation(id)
	if err != nil {
		return err
	}
	defer operation.Close()
	state, err := loadSessionState(id)
	if err != nil {
		return err
	}
	if err := p.verifySessionRuntime(state); err != nil {
		return err
	}
	if !interruptedSessionLaunch(state) {
		return fmt.Errorf("no interrupted launch requires acknowledgment")
	}
	state.InterruptedLaunch = false
	state.LaunchOperation = ""
	state.State = "cleanup_pending"
	state.Message = "Interrupted launch acknowledged; cleanup still requires live container identity and absence checks."
	return saveSessionState(*state)
}
