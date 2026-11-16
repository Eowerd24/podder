package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// managedJournal records intent before every engine mutation. It is private,
// store-bound, and survives both a lost command result and process death.
type managedJournal struct {
	Service           string        `json:"service"`
	Token             string        `json:"token"`
	Runtime           string        `json:"runtime"`
	Operation         string        `json:"operation"`
	Step              string        `json:"step"`
	Original          *Container    `json:"original,omitempty"`
	OriginalInspect   string        `json:"originalInspect,omitempty"`
	OriginalLifecycle string        `json:"originalLifecycle,omitempty"`
	Backup            string        `json:"backup,omitempty"`
	Candidate         ContainerSpec `json:"candidate"`
	CandidatePath     string        `json:"candidatePath,omitempty"`
	CandidateID       string        `json:"candidateId,omitempty"`
	OldSpec           []byte        `json:"oldSpec,omitempty"`
	OldSpecExists     bool          `json:"oldSpecExists"`
	Committed         bool          `json:"committed"`
	CommitUncertain   bool          `json:"commitUncertain,omitempty"`
	RetainBackup      bool          `json:"retainBackup"`
	lock              *os.File
	// A failed restoration may have changed the file before reporting an
	// fsync error. Matching bytes alone must not retire its recovery journal.
	rollbackSpecFailed bool
}

type ManagedRecovery struct {
	Service    string `json:"service"`
	Operation  string `json:"operation"`
	Step       string `json:"step"`
	OriginalID string `json:"originalId"`
	BackupName string `json:"backupName"`
	Message    string `json:"message"`
}

func managedJournalPath(service string) (string, error) {
	if err := validateServiceName(service); err != nil {
		return "", err
	}
	return filepath.Join(getConfigDir(), "managed-transactions", service+".json"), nil
}
func syncPrivateDirectory(path string) error {
	root, err := privateRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	return syncRootDirectory(root)
}
func (j *managedJournal) save() error {
	path, err := managedJournalPath(j.Service)
	if err != nil {
		return err
	}
	copy := *j
	stored, err := storedEnvironmentSpec(j.Candidate)
	if err != nil {
		return err
	}
	copy.Candidate = stored
	j.Candidate.EnvironmentID = stored.EnvironmentID
	j.Candidate.RequiredEnv = stored.RequiredEnv
	b, err := json.Marshal(&copy)
	if err != nil {
		return err
	}
	if err = writePrivateFileAtomic(path, b); err != nil {
		return err
	}
	return syncPrivateDirectory(filepath.Dir(path))
}
func (j *managedJournal) remove() error {
	path, err := managedJournalPath(j.Service)
	if err != nil {
		return err
	}
	root, err := privateRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(filepath.Base(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncRootDirectory(root)
}

// restoreOldSpec restores authoritative state, including durable absence for
// create/adopt when no spec existed before the transaction. Call only after
// the candidate is absent and the original runtime has been restored.
func (j *managedJournal) restoreOldSpec() error {
	final, err := getSpecFilePath(j.Service)
	if err != nil {
		return err
	}
	if j.OldSpecExists {
		if err := writePrivateFileAtomic(final, j.OldSpec); err != nil {
			return err
		}
	} else {
		root, openErr := privateRoot(filepath.Dir(final))
		if openErr != nil {
			return openErr
		}
		removeErr := root.Remove(filepath.Base(final))
		if removeErr == nil {
			removeErr = syncRootDirectory(root)
		}
		root.Close()
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
	}
	if !j.oldSpecMatches() {
		return fmt.Errorf("restored authoritative spec could not be verified")
	}
	return nil
}

func (j *managedJournal) oldSpecMatches() bool {
	final, err := getSpecFilePath(j.Service)
	if err != nil {
		return false
	}
	root, err := privateRoot(filepath.Dir(final))
	if err != nil {
		return false
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(final))
	if !j.OldSpecExists {
		return os.IsNotExist(err)
	}
	if err != nil {
		return false
	}
	var current, previous ContainerSpec
	if json.Unmarshal(data, &current) != nil || json.Unmarshal(j.OldSpec, &previous) != nil {
		return false
	}
	// Normalize optional empty fields without ContainerSpec's presentation
	// masking, which would hide changes to legacy environment values.
	type plain ContainerSpec
	a, errA := json.Marshal(plain(current))
	b, errB := json.Marshal(plain(previous))
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func (p *PodmanService) restoreManagedRollbackSpec() error {
	if p.transaction == nil {
		return nil
	}
	err := p.transaction.restoreOldSpec()
	if err != nil {
		p.transaction.rollbackSpecFailed = true
	}
	return err
}
func loadManagedJournal(service string) (*managedJournal, error) {
	path, err := managedJournalPath(service)
	if err != nil {
		return nil, err
	}
	root, err := privateRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	var j managedJournal
	if err = json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	if j.Service != service || !sessionIDPattern.MatchString(j.Token) || !sessionRuntimeIdentityPattern.MatchString(j.Runtime) {
		return nil, fmt.Errorf("invalid managed transaction journal; automatic recovery refused")
	}
	if j.CandidatePath != "" && (filepath.Dir(j.CandidatePath) != getServicesDir() || !strings.HasPrefix(filepath.Base(j.CandidatePath), service+".candidate-")) {
		return nil, fmt.Errorf("invalid candidate journal path")
	}
	return &j, nil
}
func checkManagedRecovery(service string) error {
	_, err := loadManagedJournal(service)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unfinished or unreadable managed transaction for %s; inspect recovery before retrying: %w", service, err)
	}
	return fmt.Errorf("unfinished managed transaction for %s: review Managed recoveries and explicitly recover before retrying", service)
}
func (p *PodmanService) beginManagedJournal(service, op, backup, path string, original *Container, spec ContainerSpec, acceptedInspect ...string) (*managedJournal, error) {
	if err := checkManagedRecovery(service); err != nil {
		return nil, err
	}
	identity, err := p.sessionRuntimeIdentity()
	if err != nil {
		return nil, err
	}
	if path != "" {
		dir, name, e := privatePathParts(path)
		if e != nil || dir != getServicesDir() || !isCandidateSpecFile(name) {
			return nil, fmt.Errorf("invalid candidate journal path")
		}
	}
	j := &managedJournal{Service: service, Token: newSessionID(), Runtime: identity, Operation: op, Step: "prepared", Original: original, Backup: backup, Candidate: spec, CandidatePath: path, RetainBackup: op == "adopt", lock: p.managedLock}
	if len(acceptedInspect) > 0 {
		j.OriginalInspect = acceptedInspect[0]
	}
	if original != nil {
		j.OriginalLifecycle, _ = classifyLifecycle(original.State)
	}
	final, _ := getSpecFilePath(service)
	if finalRoot, e := privateRoot(filepath.Dir(final)); e == nil {
		j.OldSpec, err = finalRoot.ReadFile(filepath.Base(final))
		finalRoot.Close()
	} else {
		err = e
	}
	j.OldSpecExists = err == nil
	if err == nil {
		var old ContainerSpec
		if e := json.Unmarshal(j.OldSpec, &old); e != nil {
			return nil, e
		}
		stored, e := storedEnvironmentSpec(old)
		if e != nil {
			return nil, e
		}
		j.OldSpec, e = json.Marshal(stored)
		if e != nil {
			return nil, e
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err = j.save(); err != nil {
		return nil, err
	}
	p.transaction = j
	return j, nil
}
func (j *managedJournal) command(r CommandRunner, args ...string) (string, string, error) {
	return j.commandWithFiles(r, nil, args...)
}

func (j *managedJournal) commandWithFiles(r CommandRunner, files []*os.File, args ...string) (string, string, error) {
	if len(args) == 0 {
		if len(files) != 0 {
			return "", "", fmt.Errorf("cannot inherit descriptors for an empty command")
		}
		return r.Run("podman", args...)
	}
	verbIndex := 0
	verb := args[verbIndex]
	if verb == "unshare" {
		// Descriptor-backed bind execution wraps the canonical inner command in
		// `podman unshare`. Locate that inner run/create so transaction intent,
		// ownership labeling, and returned-ID persistence remain unchanged.
		for i := 1; i < len(args); i++ {
			if args[i] == "run" || args[i] == "create" {
				verbIndex = i
				verb = args[i]
				break
			}
		}
	}
	mutating := verb == "rename" || verb == "stop" || verb == "start" || verb == "run" || verb == "create" || verb == "rm"
	if mutating {
		j.Step = "before-" + verb
		if err := j.save(); err != nil {
			return "", "", err
		}
		if verb == "run" || verb == "create" {
			withLabel := make([]string, 0, len(args)+2)
			withLabel = append(withLabel, args[:verbIndex+1]...)
			withLabel = append(withLabel, "--label", "io.podder.txn="+j.Token)
			args = append(withLabel, args[verbIndex+1:]...)
		}
	}
	var out, stderr string
	var err error
	if cr, ok := r.(ContextCommandRunnerWithFiles); ok && j.lock != nil && len(files) != 0 {
		out, stderr, err = cr.RunContextWithFiles(sessionCommandContext(context.Background(), j.lock), "podman", files, args...)
	} else if len(files) != 0 {
		withFiles, ok := r.(CommandRunnerWithFiles)
		if !ok {
			return "", "", fmt.Errorf("command runner cannot inherit pinned filesystem descriptors")
		}
		out, stderr, err = withFiles.RunWithFiles("podman", files, args...)
	} else if cr, ok := r.(ContextCommandRunner); ok && j.lock != nil {
		out, stderr, err = cr.RunContext(sessionCommandContext(context.Background(), j.lock), "podman", args...)
	} else {
		out, stderr, err = r.Run("podman", args...)
	}
	if mutating {
		if (verb == "run" || verb == "create") && validateFullContainerID(strings.TrimSpace(out)) == nil {
			j.CandidateID = strings.TrimSpace(out)
		}
		j.Step = "after-" + verb
		if saveErr := j.save(); saveErr != nil {
			return out, stderr, fmt.Errorf("command completed but transaction persistence failed; recovery required: %w", saveErr)
		}
	}
	return out, stderr, err
}
func (p *PodmanService) transactionCandidates(j *managedJournal) ([]Container, error) {
	cs, err := p.ListContainers(true)
	if err != nil {
		return nil, err
	}
	out := []Container{}
	for _, c := range cs {
		if c.Labels["io.podder.txn"] == j.Token {
			if !sameImageIdentity(c.ImageID, j.Candidate.ResolvedImage) || !containerMatchesSpecLabels(&c, j.Candidate) {
				return nil, fmt.Errorf("transaction marker has inconsistent ownership/image; retain %s", c.Id)
			}
			out = append(out, c)
		}
	}
	if len(out) > 1 {
		return nil, fmt.Errorf("multiple transaction candidates; retain for explicit inspection")
	}
	return out, nil
}
func (p *PodmanService) finishManagedJournal(j *managedJournal) {
	// Failure to observe a safe terminal state leaves the durable recovery offer.
	if j.CommitUncertain || j.Operation == "adopt" && j.Original != nil && strings.TrimSpace(j.OriginalInspect) == "" {
		return
	}
	candidates, err := p.transactionCandidates(j)
	if err != nil {
		return
	}
	if j.Committed && len(candidates) == 1 && verifyJournalCandidate(&candidates[0], j) && p.authoritativeJournalSpecMatches(j) {
		if j.Original != nil && !j.RetainBackup {
			cs, e := p.ListContainers(true)
			if e != nil || findContainerByExactID(cs, j.Original.Id) != nil {
				return
			}
		}
		_ = j.remove()
		return
	}
	if len(candidates) != 0 {
		return
	}
	if j.CandidateID != "" && !p.containerAbsent(j.CandidateID, "") {
		return
	}
	if j.Original != nil {
		cs, e := p.ListContainers(true)
		if e != nil {
			return
		}
		c := findContainerByExactID(cs, j.Original.Id)
		if c == nil || !containerHasName(c, j.Service) {
			return
		}
		kind, _ := classifyLifecycle(c.State)
		if kind != j.OriginalLifecycle {
			return
		}
		if j.OriginalInspect != "" && p.verifyAcceptedAdoptionOriginal(j.Original.Id, j.OriginalInspect) != nil {
			return
		}
	}
	if j.rollbackSpecFailed || !j.oldSpecMatches() {
		return
	}
	discardCandidateSpec(j.CandidatePath)
	_ = j.remove()
}
func containerHasName(c *Container, name string) bool {
	if c == nil {
		return false
	}
	for _, n := range c.Names {
		if strings.TrimPrefix(n, "/") == name {
			return true
		}
	}
	return false
}

// ListManagedRecoveries is read-only and safe to call at startup. Explicit
// recovery acquires the same lock and verifies store, full IDs and ownership.
func (p *PodmanService) ListManagedRecoveries() ([]ManagedRecovery, error) {
	dir := filepath.Join(getConfigDir(), "managed-transactions")
	root, err := privateRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	opened, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	es, err := opened.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []ManagedRecovery{}
	for _, e := range es {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		service := strings.TrimSuffix(e.Name(), ".json")
		j, err := loadManagedJournal(service)
		if err != nil {
			return nil, err
		}
		id := ""
		if j.Original != nil {
			id = j.Original.Id
		}
		out = append(out, ManagedRecovery{service, j.Operation, j.Step, id, j.Backup, "Review the exact original and candidate identities, then recover. Unrelated name occupants are never removed."})
	}
	return out, nil
}
func (p *PodmanService) RecoverManagedTransaction(service string, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("explicit managed recovery approval required")
	}
	release, err := lockManagedTransaction()
	if err != nil {
		return err
	}
	defer release()
	j, err := loadManagedJournal(service)
	if err != nil {
		return err
	}
	identity, err := p.sessionRuntimeIdentity()
	if err != nil || identity != j.Runtime {
		return fmt.Errorf("original runtime/store identity differs; recovery refused")
	}
	if j.Operation == "adopt" && j.Original != nil && strings.TrimSpace(j.OriginalInspect) == "" {
		return fmt.Errorf("adoption recovery journal lacks the accepted original inspect snapshot; automatic recovery refused")
	}
	candidates, err := p.transactionCandidates(j)
	if err != nil {
		return err
	}
	cs, err := p.ListContainers(true)
	if err != nil {
		return err
	}
	var original *Container
	if j.Original != nil {
		original = findContainerByExactID(cs, j.Original.Id)
	}
	if j.Original != nil && original == nil {
		if j.Committed && len(candidates) == 1 && verifyJournalCandidate(&candidates[0], j) && p.authoritativeJournalSpecMatches(j) {
			return j.remove()
		}
		return fmt.Errorf("original full ID is absent; retain resources for inspection")
	}
	if original != nil && (!sameContainerLabels(original.Labels, j.Original.Labels) || !sameImageIdentity(original.ImageID, j.Original.ImageID) || !containerHasName(original, j.Service) && !containerHasName(original, j.Backup)) {
		return fmt.Errorf("original identity/ownership changed; retain resources")
	}
	// An unrelated name occupant is a blocker, never deletion authority.
	occupied := findContainerByName(cs, j.Service)
	if occupied != nil && (original == nil || occupied.Id != original.Id) && (len(candidates) == 0 || occupied.Id != candidates[0].Id) {
		return fmt.Errorf("service name occupied by unrelated full ID %s; retained", occupied.Id)
	}
	for _, c := range candidates {
		if err = p.exactTransactionContainer(c.Id, "", nil, &j.Candidate); err != nil {
			return err
		}
		if c.Labels["io.podder.txn"] != j.Token {
			return fmt.Errorf("candidate marker changed")
		}
		_ = p.StopContainer(c.Id)
		if err = p.forceRemoveContainer(c.Id); err != nil {
			return err
		}
		if !p.containerAbsent(c.Id, "") {
			return fmt.Errorf("candidate removal not verified")
		}
	}
	if original != nil {
		if !containerHasName(original, j.Service) {
			if _, _, err = p.runCommand("rename", "--", original.Id, j.Service); err != nil {
				return fmt.Errorf("restore rename result uncertain; journal retained: %w", err)
			}
		}
		if j.OriginalLifecycle == lifecycleRunning {
			if err = p.StartContainer(original.Id); err != nil {
				return err
			}
		}
		cs, err = p.ListContainers(true)
		if err != nil {
			return err
		}
		restored := findContainerByExactID(cs, original.Id)
		if !containerHasName(restored, j.Service) {
			return fmt.Errorf("restored name not verified")
		}
		kind, _ := classifyLifecycle(restored.State)
		if kind != j.OriginalLifecycle {
			return fmt.Errorf("restored lifecycle not verified")
		}
		if j.OriginalInspect != "" {
			if err = p.verifyAcceptedAdoptionOriginal(j.Original.Id, j.OriginalInspect); err != nil {
				return fmt.Errorf("restored accepted controls not verified: %w", err)
			}
		}
	}
	if err := j.restoreOldSpec(); err != nil {
		return err
	}
	discardCandidateSpec(j.CandidatePath)
	return j.remove()
}

func verifyJournalCandidate(c *Container, j *managedJournal) bool {
	kind, _ := classifyLifecycle(c.State)
	want := lifecycleRunning
	if j.Original != nil {
		want = j.OriginalLifecycle
	}
	return containerHasName(c, j.Service) && kind == want && sameImageIdentity(c.ImageID, j.Candidate.ResolvedImage) && containerMatchesSpecLabels(c, j.Candidate) && mappingsExactlyEqual(c.PortMappings, j.Candidate.PortMappings)
}

// Identify a created-but-unstarted container even when Podman prints no ID.
// The unique journal token, service ownership and immutable image must all
// agree. Name occupancy alone is never candidate ownership.
func (p *PodmanService) transactionCandidateIdentity(returned string, spec ContainerSpec) (string, bool, error) {
	id := strings.TrimSpace(returned)
	if p.transaction != nil {
		candidates, err := p.transactionCandidates(p.transaction)
		if err != nil {
			return "", false, err
		}
		if len(candidates) == 1 {
			found := candidates[0].Id
			if id != "" && id != found {
				return "", false, fmt.Errorf("returned ID and transaction marker disagree; retain resources")
			}
			p.transaction.CandidateID = found
			if err = p.transaction.save(); err != nil {
				return "", false, err
			}
			return found, true, nil
		}
	}
	if id == "" {
		return "", false, nil
	}
	// A genuine returned full ID remains useful for older scripted runners;
	// destructive cleanup still separately revalidates candidate ownership.
	cs, err := p.ListContainers(true)
	if err != nil {
		return id, false, err
	}
	return id, findContainerByExactID(cs, id) != nil, nil
}

func (p *PodmanService) authoritativeJournalSpecMatches(j *managedJournal) bool {
	current, err := p.GetSpec(j.Service)
	if err != nil {
		return false
	}
	candidate := j.Candidate
	current.CreatedAt = ""
	current.UpdatedAt = ""
	candidate.CreatedAt = ""
	candidate.UpdatedAt = ""
	// Compare immutable runtime references, not their availability after reboot.
	// Recovery must still work after ephemeral environment material is gone.
	current.Env = nil
	candidate.Env = nil
	a, _ := json.Marshal(current)
	b, _ := json.Marshal(candidate)
	return string(a) == string(b)
}
