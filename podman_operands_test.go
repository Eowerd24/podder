package main

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPodmanBridgeRejectsOptionOperands(t *testing.T) {
	p := &PodmanService{runner: newFakeCommandRunner()}
	for _, value := range []string{"--all", "-a", " \t--latest", ""} {
		for _, fn := range []func(string) error{p.StartContainer, p.StopContainer, p.RestartContainer, p.RemoveContainer, p.StopAndRemoveContainer, p.RemoveImage, p.PullImage} {
			if e := fn(value); e == nil {
				t.Fatalf("option operand accepted %q", value)
			}
		}
		if _, e := p.GetContainerLogs(value); e == nil {
			t.Fatal("logs accepted option")
		}
		if _, e := p.InspectContainerForAdoption(value); e == nil {
			t.Fatal("inspect accepted option")
		}
	}
	if p.runner.(*fakeCommandRunner).CallCount() != 0 {
		t.Fatal("invalid operands reached Podman")
	}
}
func TestLifecycleRequiresFullIDAndOptionBoundary(t *testing.T) {
	r := newFakeCommandRunner()
	p := &PodmanService{runner: r}
	id := strings.Repeat("a", 64)
	for _, fn := range []func(string) error{p.StartContainer, p.StopContainer, p.RestartContainer, p.forceRemoveContainer} {
		if e := fn("service-name"); e == nil {
			t.Fatal("name accepted for lifecycle")
		}
		if e := fn(id); e != nil {
			t.Fatal(e)
		}
	}
	for _, call := range r.calls {
		if len(call) < 3 || call[len(call)-2] != "--" || call[len(call)-1] != id {
			t.Fatal("missing positional boundary", call)
		}
	}
}
func TestDurablePrivateWriteAndCandidateCommit(t *testing.T) {
	withTestHome(t)
	path := filepath.Join(getConfigDir(), "durable.json")
	if e := writePrivateFileAtomic(path, []byte("replacement")); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "replacement" {
		t.Fatal(string(b), e)
	}
	f, e := os.Stat(path)
	if e != nil || f.Mode().Perm() != 0600 {
		t.Fatal("permissions", e)
	}
	spec := ContainerSpec{Name: "durable", Image: "fixture", SchemaVersion: CurrentSpecSchemaVersion}
	candidate, e := writeCandidateSpec(spec)
	if e != nil {
		t.Fatal(e)
	}
	if e = commitCandidateSpec(candidate, spec); e != nil {
		t.Fatal(e)
	}
	if e = syncPrivateDirectory(getServicesDir()); e != nil {
		t.Fatal("directory fsync unsupported", e)
	}
}
func TestWorkflowActionsPinnedAndAttested(t *testing.T) {
	b, e := os.ReadFile(".github/workflows/build.yml")
	if e != nil {
		t.Fatal(e)
	}
	var workflow struct {
		Jobs map[string]struct {
			Environment string
			Steps       []struct {
				Uses     string
				Run      string
				If       string
				Continue any `yaml:"continue-on-error"`
				Env      map[string]string
			}
		}
	}
	if e = yaml.Unmarshal(b, &workflow); e != nil {
		t.Fatal(e)
	}
	sha := regexp.MustCompile(`@[0-9a-f]{40}$`)
	attested := false
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !sha.MatchString(step.Uses) {
				t.Fatal("floating action", step.Uses)
			}
			if name != "release" || !strings.Contains(step.Run, "scripts/provenance/sign.mjs") {
				continue
			}
			if step.If != "" || step.Continue != nil && step.Continue != false || strings.Contains(step.Run, "||") || strings.Contains(step.Run, "set +e") {
				t.Fatal("release provenance must fail closed")
			}
			sign := strings.Index(step.Run, "scripts/provenance/sign.mjs bin/podder bin/podder.sigstore.json")
			crypto := strings.Index(step.Run, "gh attestation verify bin/podder --bundle bin/podder.sigstore.json")
			source := strings.Index(step.Run, "scripts/provenance/verify.mjs bin/podder bin/provenance-verification.json")
			if sign < 0 || crypto <= sign || source <= crypto {
				t.Fatal("release must sign, cryptographically verify, and then verify source provenance")
			}
			for _, required := range []string{
				`--repo "$GITHUB_REPOSITORY"`, `--source-digest "$GITHUB_SHA"`,
				`--source-ref "$GITHUB_REF"`, `--signer-digest "$GITHUB_SHA"`,
				`--signer-workflow "$GITHUB_REPOSITORY/$POLICY_WORKFLOW"`,
				"--deny-self-hosted-runners",
				"--predicate-type https://slsa.dev/provenance/v1",
				"--format json > bin/provenance-verification.json",
				`"$SOURCE_REF" "$SOURCE_SHA" "$SOURCE_TAG_OBJECT"`,
			} {
				if !strings.Contains(step.Run[crypto:], required) {
					t.Fatalf("release provenance omits required verification %q", required)
				}
			}
			if strings.Contains(step.Run[crypto:], "--no-public-good") || strings.Contains(step.Run[crypto:], "--custom-trusted-root") {
				t.Fatal("public release provenance must use standard Sigstore trust roots")
			}
			if step.Env["POLICY_WORKFLOW"] != ".github/workflows/build.yml" || step.Env["SOURCE_REPO"] != "${{ github.repository }}" || step.Env["EXPECTED_REPOSITORY_ID"] != "${{ github.repository_id }}" || step.Env["REPOSITORY_VISIBILITY"] != "${{ github.event.repository.visibility }}" {
				t.Fatal("release provenance is not bound to its repository and signing policy")
			}
			attested = true
		}
	}
	if !attested || workflow.Jobs["release"].Environment != "release" {
		t.Fatal("missing release environment or provenance step")
	}
}
