package main

import (
	"strings"
)

// WorkloadProvenance identifies who created and manages a container.
type WorkloadProvenance struct {
	Type              string            `json:"type"`              // "podder", "compose", "quadlet", "pod", "adhoc", "unknown"
	DisplayType       string            `json:"displayType"`       // "Podder", "Compose", "Quadlet", "Pod", "Ad-Hoc", "Unknown"
	Name              string            `json:"name"`              // Project name, unit name, pod name, service name
	Service           string            `json:"service,omitempty"` // Compose service name or Podder service name
	WorkingDir        string            `json:"workingDir,omitempty"`
	ConfigFile        string            `json:"configFile,omitempty"`
	UnitName          string            `json:"unitName,omitempty"`
	PodID             string            `json:"podId,omitempty"`
	PodName           string            `json:"podName,omitempty"`
	CanMutateDirectly bool              `json:"canMutateDirectly"`
	Guidance          string            `json:"guidance,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

// ClassifyProvenance inspects container labels, environment, and pod membership to determine provenance.
func ClassifyProvenance(labels map[string]string, podID, podName string) WorkloadProvenance {
	if labels == nil {
		labels = make(map[string]string)
	}

	// 1. Podder-Managed Workloads (Highest precedence for Podder declarative specs)
	if strings.EqualFold(labels["io.podder.managed"], "true") || strings.EqualFold(labels["io.podder.managed"], "1") {
		svcName := labels["io.podder.service"]
		if svcName == "" {
			svcName = labels["io.podder.name"]
		}
		if svcName == "" {
			svcName = "Podder Service"
		}
		return WorkloadProvenance{
			Type:              "podder",
			DisplayType:       "Podder",
			Name:              svcName,
			Service:           svcName,
			CanMutateDirectly: true,
			Guidance:          "Managed declaratively by Podder. Configuration and ports can be updated directly.",
			Labels:            labels,
		}
	}

	// 2. Compose-Managed (Docker Compose or Podman Compose)
	composeProject := labels["com.docker.compose.project"]
	if composeProject == "" {
		composeProject = labels["io.podman.compose.project"]
	}

	if composeProject != "" {
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

		return WorkloadProvenance{
			Type:              "compose",
			DisplayType:       "Compose",
			Name:              displayName,
			Service:           composeService,
			WorkingDir:        workingDir,
			ConfigFile:        configFile,
			CanMutateDirectly: false,
			Guidance:          "Managed by Compose. To modify ports or configuration, update your compose file and re-run 'pod up'.",
			Labels:            labels,
		}
	}

	// 3. Quadlet / systemd-Managed
	unitName := labels["PODMAN_SYSTEMD_UNIT"]
	if unitName == "" {
		unitName = labels["io.systemd.unit"]
	}

	if unitName != "" {
		return WorkloadProvenance{
			Type:              "quadlet",
			DisplayType:       "Quadlet",
			Name:              unitName,
			UnitName:          unitName,
			CanMutateDirectly: false,
			Guidance:          "Managed by systemd / Quadlet. Update port definitions in your .container file and reload systemd ('systemctl --user daemon-reload').",
			Labels:            labels,
		}
	}

	// 4. Pod Membership (Shared network namespace)
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
		}
	}

	// 5. Ad-Hoc / Imperative CLI Container
	return WorkloadProvenance{
		Type:              "adhoc",
		DisplayType:       "Ad-Hoc",
		Name:              "Ad-Hoc CLI",
		CanMutateDirectly: false,
		Guidance:          "Created directly via CLI without an external orchestrator or declarative spec.",
		Labels:            labels,
	}
}
