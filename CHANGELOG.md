# Changelog

All notable changes to this project will be documented in this file.

## [1.2.0] - 2026-08-30

### Added
- **Structured Port Discovery (Phase 1)**: First-class `PortMapping` model parsing container published ports from Podman, handling TCP/UDP protocols, explicit host binds, and multiple mappings.
- **Container Card Port Visibility**: Container cards now clearly render published ports with protocol tags and exposure-aware styling (`loopback`, `wildcard`, `specific IP`) or show `None` when unmapped.
- **Port Administration Tab (Phase 2)**: New top-level "Ports" tab inspecting container published ports and host listening sockets side-by-side with summary metrics (published mappings, host listeners, unique ports, conflicts) and fast filtering (`All`, `Podman`, `Host`, `Conflicts`).
- **Host Listener Discovery**: Native `ss`-based socket observer on Linux discovering active TCP and UDP listening sockets and distinguishing Podman-managed listeners from host daemons.
- **Port Conflict & Exposure Engine (Phase 3)**: Pure-Go port claim analysis evaluating bind address collisions, protocol boundaries (TCP vs UDP independence), wildcard exposure warnings, and automated next-free-port suggestions.
- **Multi-Port Container Creation UX**: Upgraded Run Container modal with structured multi-row port mapping editor, auto-suggest free port helpers, live collision validation, and high-visibility wildcard exposure notices.
- **Optional External Port Registry & Reconciliation (Phase 4)**: Added resilient, read-only V1 YAML parser (`ports.yaml`), declared vs. observed reconciliation engine (`MATCH`, `UNDECLARED`, `DECLARED_MISSING`, `RESERVED_FREE`, `RESERVED_IN_USE`, `PLANNED`), registry reservation collision safeguards in `ValidatePortMapping` and `FindFreePort`, Settings modal with file picker and live validation, and reconciliation metrics in the Ports tab.
- **Management-Origin Detection & Provenance (Phase 5)**: Introduced `WorkloadProvenance` classifier inspecting container labels and pod namespaces to identify orchestrators (`Compose`, `Quadlet/systemd`, `Pod`, `Podder`, `Ad-Hoc`). Displaying provenance badges across container cards and the Ports tab with orchestrator-aware guidance tooltips, and added provenance filters in the Containers view.
- **Podder-Managed Declarative Workload Specifications (Phase 6)**: Added durable `ContainerSpec` storage format saved atomically under `$XDG_CONFIG_HOME/podder/services/<name>.json`, automatic labelling (`io.podder.managed=true`, `io.podder.service=<name>`), declarative `DeploySpec` orchestrator, spec viewer modal in UI, and "Save as Podder-Managed Workload" toggle.
- **Safe Port Mutation Transactions (Phase 7)**: Implemented transactional port editing engine (`Preflight -> Snapshot -> Rename & Launch -> Verify Socket Health -> Commit or Rollback`). Added orchestrator-aware guidance snippets for Compose (`docker-compose.yml`) and Quadlet (`.container`), Edit Ports buttons across container cards and the Ports tab, and step-by-step transaction logs in the UI.
- **Workload Adoption (Phase 9)**: Added automated inspect-to-spec engine (`InspectContainerForAdoption`, `AdoptContainer`) converting ad-hoc running containers into persistent Podder-managed declarative workloads (`$XDG_CONFIG_HOME/podder/services/<name>.json`), complete with completeness/safety analysis, blockers, warnings, and an Adopt Workload modal with spec diff preview.
- **Compose & Quadlet In-Place Integration (Phase 8)**: Added transactional in-place file mutation engines for systemd Quadlet units (`.container` `PublishPort=`, `systemctl --user daemon-reload`, unit restart with rollback) and Docker/Podman Compose project files (`ports:` YAML editing, `compose up -d` with rollback), complete with source file discovery and modal editing mode switcher.
- **Podman Network Browser & IPAM Inspector (Phase 10)**: Added dedicated Networks view inspecting local bridge/macvlan/ipvlan networks, subnets, gateways, internal isolation flags, DNS resolution status, and active container IP address allocations, with full network creation and removal management.

## [1.1.2] - 2026-07-15

### Changed
- Updated release metadata and packaging manifests to version `1.1.2`.
- GitHub Actions now publishes the Linux binary for the `v1.1.2` release tag.

## [0.1.1] - 2026-07-13

### Changed
- Podder now prefers Podman-native compose execution before falling back to plain `docker-compose`.
- `pod up` / `podder up` now preflight the rootless Podman API socket and attempt `systemctl --user start podman.socket` automatically when `podman compose` needs it.
- The Run Container modal now supports native host-folder and host-image-file picking for bind mounts instead of forcing manual path entry.
- Dashboard summary cards now behave as explicit drill-down controls for all containers, running containers, stopped containers, and images.

## [1.0.0] - 2026-07-11

### Added
- Local Go (v1.22.5) and Wails v3 CLI installation configuration.
- `PodmanService` backend in Go (`podman.go`) executing Podman CLI commands safely via `exec.Command` and returning structured JSON data.
- Full UI frontend in HTML/CSS/JS with a beautiful dark glassmorphic design system.
- Dashboard tab rendering container stats and host operating system details.
- Containers tab with controls to Start, Stop, Restart, and Remove containers.
- Terminal-like logs viewer modal with real-time refresh (3s polling).
- Run Container modal with inputs for image, name, ports, and command.
- Images tab allowing users to view local images, pull new images, run them, or delete them.
- Toast notifications for reporting action progress and errors.
- Unit test suite (`podman_test.go`) validating container, image, and system info JSON parsing.
- Comprehensive `.gitignore` configuration for Go, Wails v3, Node.js, and IDE files.
- Command-line argument parsing in `main.go` supporting `podder up` / `podder down` commands for executing `compose` commands.
- Symlinks for `podder` and `pod` globally exposed in the user's path (`/home/sarge/.local/bin/`).
