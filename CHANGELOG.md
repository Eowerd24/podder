# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

Post-audit corrective pass on the v1.3.0 hardening work.

### Changed
- Normal image deletion (`RemoveImage`) no longer passes `podman rmi --force`: it refuses when the image is in use by a container instead of silently deleting that container.
- Adoption blocks containers using `--rm`/`AutoRemove` (stopping the original to adopt it would destroy the rollback source).
- Successful adoption now retains the renamed original container, stopped, as a backup by default instead of deleting it automatically — the replacement can be configuration-equivalent while still losing writable-layer state the original held.
- Port bind addresses now preserve the operator's exact intent: an unset host bind, an explicit `0.0.0.0`, and an explicit `::` are distinct and round-trip distinctly through Podman/Compose/Quadlet publish specs, instead of all three collapsing into the same "omitted" form.
- Selecting an explicit compose file (not just a directory) in "Run Compose" now actually executes that exact file, instead of falling back to the provider's default filename discovery.
- Host-listener deduplication against a container's own ranged port mapping (e.g. `8000-8005`) is now range-aware, so a same-container port mutation no longer conflicts with its own duplicated `ss` observations for the rest of the range.
- `HostPort == 0` is a consistent policy end-to-end: a Podder-managed workload must name an explicit host port; unmanaged/ad-hoc creation may still leave it unset to let Podman auto-assign one, and the frontend and backend now agree on this.
- Creating a custom Podman network subnet now also checks for overlap with the host's own directly-connected network interfaces (e.g. the physical LAN), not just other Podman networks, and fails closed if that can't be determined.
- Port registry validation and reconciliation now account for `range_size`: an overflowing or negative range is rejected, and a runtime mapping only reconciles as `MATCH` against a registry declaration when the effective range/count agrees too, not just the start port.
- `DeleteSpec` now refuses to delete a spec while a live Podder-managed container still carries that service's ownership labels, instead of silently orphaning it.
- Declarative spec filenames are now derived directly from a validated, canonical service-name grammar instead of a lossy sanitization, so two distinct logical names can no longer collide on the same on-disk spec file.
- Generated Compose port-editing guidance now names the container's actual Compose service instead of falling back to a generic placeholder key when the real identity is available; it states plainly when the identity can't be determined rather than inventing a name.
- Documentation (README/ABOUT) no longer describes Compose/Quadlet port editing as in-place/automatic — it is discovery-and-guidance only, matching the code; added an explicit ownership matrix; corrected the documented minimum Go version to match `go.mod`.

## [1.3.0] - 2026-08-31

### Added
- Transaction-safe, schema-versioned Podder workload replay with complete bind, environment, entrypoint, command-argument, lifecycle, and port-range preservation.
- Default-deny workload adoption checks that block conversion when the source container uses settings Podder cannot reproduce safely.
- Exact post-change verification and structured rollback reporting for Podder-managed container port mutations, deployments, and adoption.
- Node-scoped external port-registry reconciliation and explicit handling of ambiguous workload provenance.
- Extensive deterministic tests backed by injectable command runners instead of the host's live Podman state.

### Changed
- Port discovery now fails closed during safety-critical validation, while the overview remains tolerant of partial discovery failures.
- Compose port editing is discovery/guidance-only: Podder identifies the exact project, single-file layout, and service, and generates a paste-ready snippet — it does not write to the compose file or run `compose up` on your behalf (see the Ownership Matrix in the README).
- Quadlet port editing is discovery/guidance-only: Podder identifies the owning user-scoped `.container` unit and generates a paste-ready `PublishPort=` snippet — it does not edit the unit file or reload/restart the service on your behalf.
- Network creation validates names, CIDRs, gateways, address families, and subnet overlap; ordinary network removal refuses to detach connected containers forcibly.
- CI now runs vet, unit tests, race tests, a pinned Wails build, and publishes only the artifact that passed those checks.
- Release identity, package metadata, module path, and license information now consistently identify Podder.

### Security
- Escaped untrusted backend data at the HTML, attribute, and inline-JavaScript boundaries; masked secret-like environment values in previews.
- WebKit sandbox disabling is now opt-in, and direct mutation of ad-hoc or ambiguously owned containers is blocked.

### Fixed
- Full published-port ranges and IPv6 addresses now round-trip through validation, mutation, Compose, Quadlet, registry, and verification paths.
- Legacy managed specs retain their ownership label when upgraded and replayed.

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
