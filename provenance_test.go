package main

import (
	"testing"
)

func TestClassifyProvenance_Podder(t *testing.T) {
	labels := map[string]string{
		"io.podder.managed": "true",
		"io.podder.service": "open-webui",
	}

	p := ClassifyProvenance(labels, "", "")
	if p.Type != "podder" {
		t.Errorf("expected type 'podder', got '%s'", p.Type)
	}
	if !p.CanMutateDirectly {
		t.Errorf("expected CanMutateDirectly to be true for Podder workloads")
	}
	if p.Name != "open-webui" {
		t.Errorf("expected name 'open-webui', got '%s'", p.Name)
	}
}

func TestClassifyProvenance_DockerCompose(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.project":             "homelab-stack",
		"com.docker.compose.service":             "n8n",
		"com.docker.compose.project.working_dir": "/opt/homelab",
	}

	p := ClassifyProvenance(labels, "", "")
	if p.Type != "compose" {
		t.Errorf("expected type 'compose', got '%s'", p.Type)
	}
	if p.CanMutateDirectly {
		t.Errorf("expected CanMutateDirectly to be false for Compose workloads")
	}
	if p.Service != "n8n" {
		t.Errorf("expected service 'n8n', got '%s'", p.Service)
	}
}

func TestClassifyProvenance_PodmanCompose(t *testing.T) {
	labels := map[string]string{
		"io.podman.compose.project": "ai-services",
		"io.podman.compose.service": "ollama",
	}

	p := ClassifyProvenance(labels, "", "")
	if p.Type != "compose" {
		t.Errorf("expected type 'compose', got '%s'", p.Type)
	}
	if p.Service != "ollama" {
		t.Errorf("expected service 'ollama', got '%s'", p.Service)
	}
}

func TestClassifyProvenance_Quadlet(t *testing.T) {
	labels := map[string]string{
		"PODMAN_SYSTEMD_UNIT": "vaultwarden.service",
	}

	p := ClassifyProvenance(labels, "", "")
	if p.Type != "quadlet" {
		t.Errorf("expected type 'quadlet', got '%s'", p.Type)
	}
	if p.UnitName != "vaultwarden.service" {
		t.Errorf("expected unit 'vaultwarden.service', got '%s'", p.UnitName)
	}
	if p.CanMutateDirectly {
		t.Errorf("expected CanMutateDirectly to be false for Quadlet workloads")
	}
}

func TestClassifyProvenance_Pod(t *testing.T) {
	p := ClassifyProvenance(nil, "abc123def456", "my-k8s-pod")
	if p.Type != "pod" {
		t.Errorf("expected type 'pod', got '%s'", p.Type)
	}
	if p.PodName != "my-k8s-pod" {
		t.Errorf("expected pod name 'my-k8s-pod', got '%s'", p.PodName)
	}
}

func TestClassifyProvenance_AdHoc(t *testing.T) {
	p := ClassifyProvenance(nil, "", "")
	if p.Type != "adhoc" {
		t.Errorf("expected type 'adhoc', got '%s'", p.Type)
	}
	if p.CanMutateDirectly {
		t.Errorf("expected CanMutateDirectly to be false for unmanaged ad-hoc containers")
	}
}

func TestClassifyProvenance_ComposeWithPodderLabelsIsAmbiguous(t *testing.T) {
	// The dangerous case: an externally-owned Compose workload also carries
	// a self-declared io.podder.managed=true label. This must never be
	// classified as directly Podder-managed.
	labels := map[string]string{
		"com.docker.compose.project": "homelab-stack",
		"com.docker.compose.service": "n8n",
		"io.podder.managed":          "true",
		"io.podder.service":          "n8n",
	}

	p := ClassifyProvenance(labels, "", "")
	if p.Type != "ambiguous" {
		t.Fatalf("expected type 'ambiguous' for conflicting Compose+Podder evidence, got %q", p.Type)
	}
	if p.CanMutateDirectly {
		t.Errorf("expected ambiguous provenance to never permit direct mutation")
	}
	if !p.Ambiguous || p.AmbiguityReason == "" {
		t.Errorf("expected Ambiguous=true with a populated AmbiguityReason, got %+v", p)
	}
}

func TestClassifyProvenance_QuadletWithPodderLabelsIsAmbiguous(t *testing.T) {
	labels := map[string]string{
		"PODMAN_SYSTEMD_UNIT": "vaultwarden.service",
		"io.podder.managed":   "true",
	}
	p := ClassifyProvenance(labels, "", "")
	if p.Type != "ambiguous" {
		t.Fatalf("expected type 'ambiguous' for conflicting Quadlet+Podder evidence, got %q", p.Type)
	}
}

func TestClassifyProvenance_ComposeAndQuadletBothPresentIsAmbiguous(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.project": "stack",
		"PODMAN_SYSTEMD_UNIT":        "unit.service",
	}
	p := ClassifyProvenance(labels, "", "")
	if p.Type != "ambiguous" {
		t.Fatalf("expected type 'ambiguous' when Compose and Quadlet markers coexist, got %q", p.Type)
	}
}

func TestClassifyProvenance_PodTakesPrecedenceOverLabels(t *testing.T) {
	// Pod membership is a runtime-structural fact; it must win even if
	// Podder labels are also present, since port bindings genuinely belong
	// to the Pod regardless of any label.
	labels := map[string]string{"io.podder.managed": "true"}
	p := ClassifyProvenance(labels, "pod-id-1", "my-pod")
	if p.Type != "pod" {
		t.Fatalf("expected Pod membership to take precedence, got %q", p.Type)
	}
}

func TestClassifyProvenance_ConfidenceAndEvidencePopulated(t *testing.T) {
	p := ClassifyProvenance(map[string]string{"io.podder.managed": "true"}, "", "")
	if p.Confidence != "high" || len(p.Evidence) == 0 {
		t.Errorf("expected high confidence with evidence for a clean Podder label, got %+v", p)
	}

	adhoc := ClassifyProvenance(nil, "", "")
	if adhoc.Confidence == "" || len(adhoc.Evidence) == 0 {
		t.Errorf("expected the ad-hoc fallback to still report confidence/evidence, got %+v", adhoc)
	}
}
