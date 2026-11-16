package main

import (
	"strings"
)

// WorkloadProvenance identifies who created and manages a container.
//
// Classification collects evidence from independent sources (Pod
// membership, Compose labels, Quadlet/systemd labels, Podder's own labels)
// rather than accepting the first matching marker. External ownership
// (Compose/Quadlet) always takes precedence over a self-declared Podder
// label, and conflicting evidence (e.g. io.podder.managed=true alongside
// Compose project labels) is classified "ambiguous" and treated as
// read-only rather than guessed at.
type WorkloadProvenance struct {
	Type              string            `json:"type"`        // "podder", "compose", "quadlet", "pod", "adhoc", "ambiguous"
	DisplayType       string            `json:"displayType"` // "Podder", "Compose", "Quadlet", "Pod", "Ad-Hoc", "Ambiguous"
	Name              string            `json:"name"`        // Project name, unit name, pod name, service name
	Project           string            `json:"project,omitempty"`
	Service           string            `json:"service,omitempty"` // Compose service name or Podder service name
	WorkingDir        string            `json:"workingDir,omitempty"`
	ConfigFile        string            `json:"configFile,omitempty"`
	UnitName          string            `json:"unitName,omitempty"`
	PodID             string            `json:"podId,omitempty"`
	PodName           string            `json:"podName,omitempty"`
	CanMutateDirectly bool              `json:"canMutateDirectly"`
	Guidance          string            `json:"guidance,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	// Confidence, Evidence, Ambiguous, and AmbiguityReason expose *why* a
	// classification was reached, so mutation eligibility (and the UI) can
	// reason about more than just Type. Mutation eligibility must never
	// depend solely on labels supplied by the container — it also checks
	// that an authoritative spec exists and validates (see mutations.go).
	Confidence      string   `json:"confidence"` // "high", "medium", "low"
	Evidence        []string `json:"evidence,omitempty"`
	Ambiguous       bool     `json:"ambiguous,omitempty"`
	AmbiguityReason string   `json:"ambiguityReason,omitempty"`
}

func detectCompose(labels map[string]string) (bool, WorkloadProvenance) {
	composeProject := labels["com.docker.compose.project"]
	if composeProject == "" {
		composeProject = labels["io.podman.compose.project"]
	}
	if composeProject == "" {
		return false, WorkloadProvenance{}
	}

	composeService := labels["com.docker.compose.service"]
	if composeService == "" {
		composeService = labels["io.podman.compose.service"]
	}
	workingDir := labels["com.docker.compose.project.working_dir"]
	if workingDir == "" {
		workingDir = labels["io.podman.compose.project.working_dir"]
	}
	configFile := labels["com.docker.compose.project.config_files"]
	if configFile == "" {
		configFile = labels["io.podman.compose.project.config_files"]
	}

	displayName := composeProject
	if composeService != "" {
		displayName = composeProject + " / " + composeService
	}

	return true, WorkloadProvenance{
		Type:              "compose",
		DisplayType:       "Compose",
		Name:              displayName,
		Project:           composeProject,
		Service:           composeService,
		WorkingDir:        workingDir,
		ConfigFile:        configFile,
		CanMutateDirectly: false,
		Guidance:          "Managed by Compose. To modify ports or configuration, update your compose file and re-run 'pod up'.",
		Labels:            labels,
	}
}

func detectQuadlet(labels map[string]string) (bool, WorkloadProvenance) {
	unitName := labels["PODMAN_SYSTEMD_UNIT"]
	if unitName == "" {
		unitName = labels["io.systemd.unit"]
	}
	if unitName == "" {
		return false, WorkloadProvenance{}
	}

	return true, WorkloadProvenance{
		Type:              "quadlet",
		DisplayType:       "Quadlet",
		Name:              unitName,
		UnitName:          unitName,
		CanMutateDirectly: false,
		Guidance:          "Managed by systemd / Quadlet. Update port definitions in your .container file and reload systemd ('systemctl --user daemon-reload').",
		Labels:            labels,
	}
}

func detectPodder(labels map[string]string) (bool, WorkloadProvenance) {
	if !strings.EqualFold(labels["io.podder.managed"], "true") && !strings.EqualFold(labels["io.podder.managed"], "1") {
		return false, WorkloadProvenance{}
	}

	svcName := labels["io.podder.service"]
	if svcName == "" {
		svcName = labels["io.podder.name"]
	}
	if svcName == "" {
		svcName = "Podder Service"
	}

	return true, WorkloadProvenance{
		Type:              "podder",
		DisplayType:       "Podder",
		Name:              svcName,
		Service:           svcName,
		CanMutateDirectly: true,
		Guidance:          "Managed declaratively by Podder. Configuration and ports can be updated directly.",
		Labels:            labels,
	}
}

// ClassifyProvenance inspects container labels, environment, and pod
// membership to determine provenance. It collects evidence from every
// source before deciding, rather than accepting the first marker found:
//
//   - Pod membership is a runtime-structural fact (not a label anyone can
//     forge) and always takes precedence: port bindings genuinely belong to
//     the Pod regardless of any label present.
//   - Compose and Quadlet/systemd labels are external-ownership evidence
//     and always outrank a self-declared io.podder.managed label.
//   - If external evidence (Compose or Quadlet) coexists with Podder's own
//     labels, or Compose and Quadlet markers coexist with each other, that
//     is genuinely conflicting evidence: the result is classified
//     "ambiguous" and treated as read-only rather than guessed at.
//   - The "ad-hoc" fallback means "no recognized external owner detected",
//     not an assertion of high-certainty ownership.
func ClassifyProvenance(labels map[string]string, podID, podName string) WorkloadProvenance {
	if labels == nil {
		labels = make(map[string]string)
	}

	trimmedPodID := strings.TrimSpace(podID)
	trimmedPodName := strings.TrimSpace(podName)
	if trimmedPodID != "" || trimmedPodName != "" {
		pName := trimmedPodName
		if pName == "" {
			pName = trimmedPodID
			if len(pName) > 12 {
				pName = pName[:12]
			}
		}
		return WorkloadProvenance{
			Type:              "pod",
			DisplayType:       "Pod",
			Name:              pName,
			PodID:             trimmedPodID,
			PodName:           trimmedPodName,
			CanMutateDirectly: false,
			Guidance:          "Container is a member of a Pod. Network ports are bound and shared at the Pod level, not per container.",
			Labels:            labels,
			Confidence:        "high",
			Evidence:          []string{"Pod membership (runtime structural fact)"},
		}
	}

	hasCompose, composeProv := detectCompose(labels)
	hasQuadlet, quadletProv := detectQuadlet(labels)
	hasPodder, podderProv := detectPodder(labels)

	externalConflict := (hasCompose || hasQuadlet) && hasPodder
	crossExternalConflict := hasCompose && hasQuadlet

	if externalConflict || crossExternalConflict {
		var evidence []string
		if hasCompose {
			evidence = append(evidence, "Compose project/service labels")
		}
		if hasQuadlet {
			evidence = append(evidence, "Quadlet/systemd unit labels")
		}
		if hasPodder {
			evidence = append(evidence, "self-declared io.podder.managed label")
		}
		return WorkloadProvenance{
			Type:              "ambiguous",
			DisplayType:       "Ambiguous",
			Name:              "Conflicting ownership evidence",
			CanMutateDirectly: false,
			Guidance:          "This container carries conflicting ownership evidence and Podder cannot safely determine the true owner. Treat it as read-only until the conflicting labels are resolved.",
			Labels:            labels,
			Confidence:        "low",
			Ambiguous:         true,
			AmbiguityReason:   "Multiple independent ownership markers are present on this container: " + strings.Join(evidence, ", ") + ".",
			Evidence:          evidence,
		}
	}

	switch {
	case hasCompose:
		composeProv.Confidence = "high"
		composeProv.Evidence = []string{"Compose project/service labels"}
		return composeProv
	case hasQuadlet:
		quadletProv.Confidence = "high"
		quadletProv.Evidence = []string{"PODMAN_SYSTEMD_UNIT / io.systemd.unit label"}
		return quadletProv
	case hasPodder:
		podderProv.Confidence = "high"
		podderProv.Evidence = []string{"io.podder.managed label"}
		return podderProv
	default:
		return WorkloadProvenance{
			Type:              "adhoc",
			DisplayType:       "Ad-Hoc",
			Name:              "Ad-Hoc CLI",
			CanMutateDirectly: false,
			Guidance:          "No recognized external owner detected. Created directly via CLI without an external orchestrator or declarative spec.",
			Labels:            labels,
			Confidence:        "medium",
			Evidence:          []string{"no recognized ownership label found"},
		}
	}
}
