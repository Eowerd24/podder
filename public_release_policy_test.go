package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type publicPolicyStep struct {
	Uses             string
	Run              string
	If               string
	Continue         any `yaml:"continue-on-error"`
	With             map[string]any
	Env              map[string]string
	WorkingDirectory string `yaml:"working-directory"`
}

type publicPolicyJob struct {
	If          string
	Needs       any
	Environment string
	Permissions map[string]string
	RunsOn      string `yaml:"runs-on"`
	Steps       []publicPolicyStep
}

type publicPolicyWorkflow struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]string
	Jobs        map[string]publicPolicyJob
}

func readPublicPolicy(t *testing.T) publicPolicyWorkflow {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/build.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow publicPolicyWorkflow
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func publicStepContaining(t *testing.T, steps []publicPolicyStep, command string) (int, publicPolicyStep) {
	t.Helper()
	for index, step := range steps {
		if strings.Contains(step.Run, command) {
			return index, step
		}
	}
	t.Fatalf("missing required command %q", command)
	return -1, publicPolicyStep{}
}

func publicAction(t *testing.T, steps []publicPolicyStep, action string) (int, publicPolicyStep) {
	t.Helper()
	for index, step := range steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			return index, step
		}
	}
	t.Fatalf("missing required action %s", action)
	return -1, publicPolicyStep{}
}

func requirePublicRun(t *testing.T, step publicPolicyStep, fragments ...string) {
	t.Helper()
	if step.If != "" || step.Continue != nil && step.Continue != false || strings.Contains(step.Run, "||") || strings.Contains(step.Run, "set +e") {
		t.Fatal("required release command can be skipped or its failure ignored")
	}
	for _, fragment := range fragments {
		if !strings.Contains(step.Run, fragment) {
			t.Errorf("required release command omits %q", fragment)
		}
	}
}

func TestPublicReleaseSourceAndBuildGates(t *testing.T) {
	workflow := readPublicPolicy(t)
	build := workflow.Jobs["test"]
	if build.If != "" {
		t.Fatal("normal build gates must run for every configured event")
	}
	_, checkout := publicAction(t, build.Steps, "actions/checkout")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != 0 || checkout.With["persist-credentials"] != false {
		t.Fatal("checkout lost event commit, signed tag history, or credential isolation")
	}
	sourceIndex, source := publicStepContaining(t, build.Steps, "python3 scripts/verify-release.py")
	if source.If != "startsWith(github.ref, 'refs/tags/v')" || strings.TrimSpace(source.Run) != "python3 scripts/verify-release.py" || source.Continue != nil {
		t.Fatal("release tag verification must gate every tag build")
	}
	for _, command := range []string{
		"node --test scripts/provenance/*.test.mjs", "npm audit --audit-level=low", "npm test", "go vet ./...",
		"-mode=source -scan=symbol ./...", "go test ./...",
		"go test -tags integration -run '^$' ./...",
		"go test -tags server -run '^TestServerRuntimeKeepsUpstreamOriginPolicy$' .",
		"go test -race .", "wails3 build",
	} {
		index, step := publicStepContaining(t, build.Steps, command)
		requirePublicRun(t, step, command)
		if index <= sourceIndex {
			t.Errorf("%q executes before signed tag verification", command)
		}
		if command == "npm test" && step.WorkingDirectory != "./frontend" {
			t.Fatal("frontend tests must check application frontend dependencies")
		}
	}
	if _, err := os.Stat(".github/workflows/release-recovery.yml"); !os.IsNotExist(err) {
		t.Fatal("discarded-history recovery workflow must not be available for dispatch")
	}
}

func TestPublicReleaseVerificationAndPublicationPolicy(t *testing.T) {
	workflow := readPublicPolicy(t)
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatal("workflow defaults must remain read-only")
	}
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" {
			t.Fatalf("%s is no longer confined to the hosted build runner", name)
		}
		if name != "release" && len(job.Permissions) != 0 {
			t.Fatal("unprivileged build jobs must inherit read-only permissions")
		}
		for _, step := range job.Steps {
			if step.Continue != nil && step.Continue != false {
				t.Fatal("release work cannot ignore a failed step")
			}
			if step.Uses != "" && !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(step.Uses) {
				t.Fatalf("action is not pinned to an immutable revision: %s", step.Uses)
			}
		}
	}
	release := workflow.Jobs["release"]
	if release.If != "startsWith(github.ref, 'refs/tags/v')" || release.Needs != "test" || release.Environment != "release" || len(release.Permissions) != 2 || release.Permissions["contents"] != "write" || release.Permissions["id-token"] != "write" {
		t.Fatal("publishing must depend on successful tag-build gates and scoped release permissions")
	}
	_, checkout := publicAction(t, release.Steps, "actions/checkout")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != 0 || checkout.With["persist-credentials"] != false {
		t.Fatal("release checkout must preserve signed source verification")
	}
	sourceIndex, source := publicStepContaining(t, release.Steps, "python3 scripts/verify-release.py")
	requirePublicRun(t, source, "python3 scripts/verify-release.py", `git rev-parse "$GITHUB_REF"`)
	_, download := publicAction(t, release.Steps, "actions/download-artifact")
	_, upload := publicAction(t, workflow.Jobs["test"].Steps, "actions/upload-artifact")
	if download.With["name"] != upload.With["name"] || download.With["path"] != "bin" {
		t.Fatal("release no longer consumes its tested build artifact")
	}
	for _, key := range []string{"run-id", "repository", "github-token"} {
		if _, set := download.With[key]; set {
			t.Fatalf("download can select an artifact outside the successful current run via %s", key)
		}
	}
	signIndex, sign := publicStepContaining(t, release.Steps, "scripts/provenance/sign.mjs")
	requirePublicRun(t, sign,
		"gh attestation verify bin/podder --bundle bin/podder.sigstore.json",
		`--repo "$GITHUB_REPOSITORY"`, `--source-digest "$GITHUB_SHA"`,
		`--source-ref "$GITHUB_REF"`, `--signer-digest "$GITHUB_SHA"`,
		`--signer-workflow "$GITHUB_REPOSITORY/$POLICY_WORKFLOW"`,
		"--deny-self-hosted-runners", "--format json", "--predicate-type https://slsa.dev/provenance/v1",
		"scripts/provenance/verify.mjs bin/podder bin/provenance-verification.json",
		`"$SOURCE_REF" "$SOURCE_SHA" "$SOURCE_TAG_OBJECT"`)
	if strings.Contains(sign.Run, "--no-public-good") || strings.Contains(sign.Run, "--custom-trusted-root") {
		t.Fatal("public release must use normal Sigstore trust roots")
	}
	if strings.Index(sign.Run, "scripts/provenance/sign.mjs") >= strings.Index(sign.Run, "gh attestation verify") || strings.Index(sign.Run, "gh attestation verify") >= strings.Index(sign.Run, "scripts/provenance/verify.mjs") {
		t.Fatal("semantic source verification must follow successful signing and cryptographic verification")
	}
	for key, expected := range map[string]string{
		"POLICY_WORKFLOW": ".github/workflows/build.yml", "SOURCE_REPO": "${{ github.repository }}",
		"SOURCE_REF": "${{ github.ref }}", "SOURCE_SHA": "${{ github.sha }}",
		"EXPECTED_REPOSITORY_ID": "${{ github.repository_id }}", "REPOSITORY_VISIBILITY": "${{ github.event.repository.visibility }}",
	} {
		if sign.Env[key] != expected {
			t.Errorf("%s must come from the trusted workflow context", key)
		}
	}
	draftIndex, draft := publicAction(t, release.Steps, "softprops/action-gh-release")
	if draft.If != "" || draft.With["draft"] != true {
		t.Fatal("assets must first be uploaded to a draft release")
	}
	for _, asset := range []string{"bin/podder", "bin/podder.sigstore.json", "bin/SHA256SUMS"} {
		if !strings.Contains(fmt.Sprint(draft.With["files"]), asset+"\n") && strings.TrimSpace(fmt.Sprint(draft.With["files"])) != asset {
			t.Errorf("release omits required verified asset %s", asset)
		}
	}
	publishIndex, publish := publicStepContaining(t, release.Steps, "gh release edit")
	requirePublicRun(t, publish, `gh release edit "$RELEASE_TAG" --draft=false --latest`, `test "$immutable" = true`)
	if sourceIndex >= signIndex || signIndex >= draftIndex || draftIndex >= publishIndex {
		t.Fatal("publication must follow signed source verification, provenance verification, and complete draft assets")
	}
}
