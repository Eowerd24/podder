package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pinnedMountNamespaceRunner interface {
	podmanInnerPrefix() []string
}

type pinnedMountPlan struct {
	base            string
	pairs           [][3]string
	files           []*os.File
	innerPrefix     []string
	barrierReady    string
	barrierContinue string
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func canonicalPrivatePath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

func protectedPodderPaths() []string {
	return []string{
		canonicalPrivatePath(getConfigDir()),
		canonicalPrivatePath(sessionDataDir()),
		canonicalPrivatePath(sessionRuntimeDir()),
	}
}

func rejectPrivateRootOverlap(path string) error {
	clean := filepath.Clean(path)
	candidates := []string{clean}
	if physical, err := filepath.EvalSymlinks(clean); err == nil && physical != clean {
		candidates = append(candidates, filepath.Clean(physical))
	}
	for _, protected := range protectedPodderPaths() {
		for _, candidate := range candidates {
			if pathContains(candidate, protected) || pathContains(protected, candidate) {
				return fmt.Errorf("bind overlaps protected Podder private storage: %s", protected)
			}
		}
	}
	return nil
}

func approveExternalSessionBinds(spec *DisposableSessionSpec) error {
	if len(spec.ExternalBinds) == 0 {
		spec.ExternalBindApprovals = nil
		return nil
	}
	if !descriptorMountHandoffAvailable() {
		return fmt.Errorf("external binds require descriptor-backed mount handoff on this platform")
	}
	approvals := make([]SessionBindApproval, 0, len(spec.ExternalBinds))
	for i := range spec.ExternalBinds {
		bind := &spec.ExternalBinds[i]
		physical, err := filepath.EvalSymlinks(bind.HostPath)
		if err != nil {
			return fmt.Errorf("external bind cannot be resolved for approval: %w", err)
		}
		physical = filepath.Clean(physical)
		if sensitiveMountPath(physical) || rejectPrivateRootOverlap(physical) != nil {
			return fmt.Errorf("external bind resolves to a protected directory")
		}
		pinned, err := openPinnedDirectory(physical, "")
		if err != nil {
			return fmt.Errorf("external bind cannot be securely opened: %w", err)
		}
		identity, idErr := openFileIdentity(pinned)
		pinned.Close()
		if idErr != nil {
			return idErr
		}
		bind.HostPath = physical
		approvals = append(approvals, SessionBindApproval{HostPath: physical, ContainerPath: bind.ContainerPath, ReadOnly: bind.ReadOnly, ObjectIdentity: identity})
	}
	spec.ExternalBindApprovals = approvals
	return nil
}

func verifyExternalSessionBindApprovals(spec *DisposableSessionSpec) error {
	if len(spec.ExternalBinds) == 0 {
		if len(spec.ExternalBindApprovals) != 0 {
			return fmt.Errorf("unexpected external bind approval witness")
		}
		return nil
	}
	if len(spec.ExternalBindApprovals) != len(spec.ExternalBinds) {
		return fmt.Errorf("generate a fresh launch preview to approve the exact external filesystem objects")
	}
	for i := range spec.ExternalBinds {
		bind := &spec.ExternalBinds[i]
		approval := spec.ExternalBindApprovals[i]
		if bind.ContainerPath != approval.ContainerPath || bind.ReadOnly != approval.ReadOnly {
			return fmt.Errorf("external bind changed after preview")
		}
		physical, err := filepath.EvalSymlinks(bind.HostPath)
		if err != nil || filepath.Clean(physical) != approval.HostPath {
			return fmt.Errorf("external bind pathname changed after preview")
		}
		if sensitiveMountPath(physical) || rejectPrivateRootOverlap(physical) != nil {
			return fmt.Errorf("external bind resolves to a protected directory")
		}
		pinned, err := openPinnedDirectory(physical, approval.ObjectIdentity)
		if err != nil {
			return err
		}
		pinned.Close()
		bind.HostPath = physical
	}
	return nil
}

// approveContainerSpecBinds canonicalizes and records the exact directory
// objects that an ordinary create/replay/adoption transaction intends to
// expose. The descriptors are reopened against these witnesses immediately
// before execution and remain open through Podman's mount handoff.
func approveContainerSpecBinds(spec *ContainerSpec) error {
	if len(spec.Binds) != 0 && !descriptorMountHandoffAvailable() {
		return fmt.Errorf("container binds require descriptor-backed mount handoff on this platform")
	}
	for i := range spec.Binds {
		physical, err := filepath.EvalSymlinks(spec.Binds[i].HostPath)
		if err != nil {
			return fmt.Errorf("bind source cannot be resolved securely: %w", err)
		}
		physical = filepath.Clean(physical)
		if err := rejectPrivateRootOverlap(physical); err != nil {
			return err
		}
		expected := spec.Binds[i].ObjectIdentity
		if expected == "" {
			expected = spec.Binds[i].sourceIdentity
		}
		pinned, err := openPinnedFilesystemObject(physical, expected)
		if err != nil {
			return fmt.Errorf("bind source must be a securely openable regular file or directory: %w", err)
		}
		identity, identityErr := openFileIdentity(pinned)
		pinned.Close()
		if identityErr != nil {
			return identityErr
		}
		spec.Binds[i].HostPath = physical
		spec.Binds[i].ObjectIdentity = identity
		spec.Binds[i].sourceIdentity = identity
	}
	return nil
}

func requirePersistedBindObjectWitnesses(spec *ContainerSpec) error {
	for _, bind := range spec.Binds {
		if strings.TrimSpace(bind.ObjectIdentity) == "" {
			return fmt.Errorf("stored bind %s has no filesystem-object approval witness; recreate it after reviewing the current source", bind.HostPath)
		}
	}
	return nil
}

func pinApprovedContainerBinds(binds []BindMountSpec) ([]BindMountSpec, func(), error) {
	pinned := make([]BindMountSpec, 0, len(binds))
	files := []*os.File{}
	closeAll := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, bind := range binds {
		expected := bind.ObjectIdentity
		if expected == "" {
			expected = bind.sourceIdentity
		}
		if expected == "" {
			closeAll()
			return nil, func() {}, fmt.Errorf("bind filesystem-object approval is missing")
		}
		file, err := openPinnedFilesystemObject(bind.HostPath, expected)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		anchor, err := pinnedMountPath(file)
		if err != nil {
			file.Close()
			closeAll()
			return nil, func() {}, err
		}
		files = append(files, file)
		bind.approvedPath = bind.HostPath
		bind.HostPath = anchor
		bind.sourceIdentity = ""
		bind.ObjectIdentity = ""
		bind.sourceFile = file
		pinned = append(pinned, bind)
	}
	return pinned, closeAll, nil
}

func closeSessionBindSources(binds []BindMountSpec) {
	for i := range binds {
		if binds[i].sourceFile != nil {
			_ = binds[i].sourceFile.Close()
			binds[i].sourceFile = nil
		}
	}
}

func pinSessionBinds(external []BindMountSpec, approvals []SessionBindApproval, generated []BindMountSpec) ([]BindMountSpec, func(), error) {
	all := make([]BindMountSpec, 0, len(external)+len(generated))
	files := []*os.File{}
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	pin := func(bind BindMountSpec, identity string) error {
		f, err := openPinnedDirectory(bind.HostPath, identity)
		if err != nil {
			return err
		}
		anchor, err := pinnedMountPath(f)
		if err != nil {
			f.Close()
			return err
		}
		files = append(files, f)
		bind.approvedPath = bind.HostPath
		bind.HostPath = anchor
		bind.sourceIdentity = ""
		bind.sourceFile = f
		all = append(all, bind)
		return nil
	}
	if len(external) != len(approvals) {
		return nil, closeAll, fmt.Errorf("external bind approval witness mismatch")
	}
	for i, bind := range external {
		if err := pin(bind, approvals[i].ObjectIdentity); err != nil {
			closeAll()
			return nil, func() {}, err
		}
	}
	for i := range generated {
		bind := generated[i]
		if bind.sourceIdentity == "" || bind.sourceFile == nil {
			closeAll()
			return nil, func() {}, fmt.Errorf("generated mount descriptor or identity is missing")
		}
		identity, err := openFileIdentity(bind.sourceFile)
		if err != nil || identity != bind.sourceIdentity {
			closeAll()
			return nil, func() {}, fmt.Errorf("generated mount filesystem object changed")
		}
		anchor, err := pinnedMountPath(bind.sourceFile)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		files = append(files, bind.sourceFile)
		generated[i].sourceFile = nil
		bind.approvedPath = bind.HostPath
		bind.sourceIdentity = ""
		bind.HostPath = anchor
		all = append(all, bind)
	}
	return all, closeAll, nil
}

// preparePinnedMountPlan gives production Podman a stable ordinary pathname
// without asking the OCI runtime to bind a procfs magic link (crun rejects
// that). `podman unshare` bind-mounts each inherited source descriptor over
// its approved pathname only inside a private mount namespace. The inner
// Podman therefore records the accepted pathname while opening the pinned
// filesystem object, even if the host-namespace pathname was replaced.
func (p *PodmanService) preparePinnedMountPlan(id string, pinned []BindMountSpec) ([]BindMountSpec, pinnedMountPlan, error) {
	provider, ok := p.cmdRunner().(pinnedMountNamespaceRunner)
	if !ok {
		// Scripted unit runners never create a process or dereference the path.
		return pinned, pinnedMountPlan{}, nil
	}
	out := append([]BindMountSpec{}, pinned...)
	plan := pinnedMountPlan{
		base: id, innerPrefix: append([]string{}, provider.podmanInnerPrefix()...),
		barrierReady: p.mountHandoffReadyPath, barrierContinue: p.mountHandoffContinuePath,
	}
	for i := range out {
		if out[i].sourceFile == nil {
			return nil, pinnedMountPlan{}, fmt.Errorf("pinned mount descriptor is missing")
		}
		if out[i].approvedPath == "" {
			return nil, pinnedMountPlan{}, fmt.Errorf("approved mount pathname is missing")
		}
		plan.files = append(plan.files, out[i].sourceFile)
		plan.pairs = append(plan.pairs, [3]string{fmt.Sprintf("/proc/self/fd/%d", 3+i), out[i].approvedPath, out[i].ContainerPath})
		out[i].HostPath = out[i].approvedPath
		out[i].sourceFile = nil
		out[i].podderGenerated = true
	}
	return out, plan, nil
}

func cleanupPinnedMountPlan(plan pinnedMountPlan) {
	// No host-namespace alias is created. The bind overlays disappear with
	// the private `podman unshare` mount namespace.
}
