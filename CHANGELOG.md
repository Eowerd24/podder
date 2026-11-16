# Changelog

Notable product changes are listed by version. Historical entries describe the scope of that version; see the README for current behavior and limitations.

## [1.5.5] - 2026-11-16 — Dependency and native bridge security

- Upgrade to Go 1.26.11, Wails 3.0.0 with the matching frontend runtime, and Vite 6.4.6 with patched transitive dependencies.
- Check frontend dependency advisories in CI alongside reachable Go vulnerabilities.
- Restrict native desktop runtime calls to Podder's document origin and authenticated JSON transport. Preserve the independent origin policy for server builds.
- Add browser policies limiting framing, child documents, plugins, form navigation and base-URL changes. External application endpoints open in the system browser; these policies do not sandbox arbitrary same-origin scripts.
- Add bridge, startup and native WebKitGTK regression fixtures for development and production modes.
- Physical power-loss behavior, newer Podman versions and all other-platform native runtime combinations are not fully validated.

## [1.5.4] - 2026-10-22 — Filesystem and adoption assurance hardening

- Bind approved external and generated mounts to stable filesystem identities and verify them before workload startup.
- Anchor private configuration, runtime, managed-spec and journal operations to securely opened directories; reject symlink traversal and protected-root overlap.
- Preserve private permissions and synchronize journal directory entries before resources can be consumed.
- Use verified immutable image IDs for cleanup, reject unsupported replacement settings, and verify accepted settings after rollback.
- Restore prior authoritative specs after persistence failures and retain recovery information when durability is uncertain.
- Keep raw inspect data out of renderer-facing adoption responses.
- Add filesystem, rollback, mount, cleanup and adoption regressions, including isolated Podman tests.
- Upgrade the build and CI toolchain to Go 1.26.10 for certificate-verification security fixes.

## [1.5.3] - 2026-10-20 — Signed release checkout correction

- Preserve annotated tag objects in CI by checking out the event commit directly, retaining signature, version and provenance checks.
- Carry forward the environment handling, renderer changes and isolated rootless tests listed under 1.5.2.
- Advance packaging metadata to 1.5.3. The earlier 1.5.2 tag did not produce a published release or assets because its workflow failed before publication.

## [1.5.2] - 2026-10-20 — Assurance follow-up (tagged; publication blocked)

- Resume the 1.x version line above 1.5.0 after the historical 0.15.1 numbering error.
- Transport managed and session environment values through private runtime files, mask previews and JSON, and allow re-entry when runtime material is missing.
- Reject line breaks and NUL characters in environment-file values.
- Replace inline HTML event handlers with delegated listeners and escape displayed port protocols and ranges.
- Verify trusted SSH-signed annotated tags, exact source versions and increasing release versions in CI.
- Add rootless crash, adoption, environment transport, filesystem and process-lifetime regressions. Physical power-loss tests remain opt-in for a disposable VM; they were not executed for this version.

## [0.15.1] - 2026-10-20 — Assurance fixes

- Reject GitHub builds when denied RUN networking cannot be enforced. Image pulls, remote ADD and other builder fetches remain separate from RUN-network consent.
- Bind port editor submissions to the expected container and revision; normalize supported localhost reservation aliases.
- Add durable managed-transaction journals, cross-process serialization, explicit recovery and cleanup bound to exact container identities.
- Support compatible legacy adoption formats, reject unsupported controls, and preserve accepted user and resource-limit settings during replay.
- Validate Quadlet identifiers and require full container IDs for lifecycle operations.
- Tie Linux SSH forwarding helpers to parent lifetime while retaining strict host-key checks.
- Pin Go 1.26.9 and workflow actions, synchronize atomic writes, and add reachable-vulnerability and build-provenance checks.
- Add isolated rootless Podman 4.9.3 regression coverage. These tests did not establish compatibility with every runtime or validate physical power-loss behavior.

## [1.5.0] - 2026-10-18 — Application / session roadmap implementation

- Add Open Web and Choose Endpoint actions, persistent launcher preferences, stale-association review, Copy URL and bounded Start & Open checks.
- Add disposable rootless sessions with explicit ports, configuration/data retention, private recipes, command previews and scoped cleanup.
- Add explicit keep-id mapping for supported disposable-session mounts and preserve recipe endpoints, mounts and favorites across relaunches.
- Add immutable public GitHub checkout review, explicit build approval, cancellable rootless builds, redacted logs and conservative image cleanup.
- Add authenticated Xpra viewer configuration, explicit terminal commands, independent SSH profiles and visible loopback forwarding helpers.
- Add recipe favorites, import/export, an OliveTin starter and JSON-based headless session commands.
- Bind session cleanup to its original Podman store, serialize builders across processes, and require explicit interrupted-build recovery.
- Restrict build contexts to reviewed checkouts, reject unsafe GitHub metadata, and return recovery JSON with failure status for unsuccessful headless operations.
- Fix native Linux select controls, dismissible launch notices and text selection in previews.
- Add deterministic backend/frontend tests, optional renderer tests with mocked operations and separately gated isolated Podman smoke tests. Platform coverage remains limited.

## [1.4.2] - 2026-09-01

- Require approved filesystem roots before reading container-reported Compose files, and validate Quadlet identifiers and discovered paths.
- Add workload-owner checks and explicit lifecycle, owner mismatch and drift states to port-registry reconciliation.
- Refresh port editor data from the exact container ID and generate Compose/Quadlet guidance through shared backend formatting.
- Preserve omitted, wildcard, loopback and specific bind declarations distinctly during verification; support port ranges throughout the UI.
- Make invalid enabled port-registry entries block safety-critical validation while preserving warnings in observation views.
- Present Compose and Quadlet port editing as guidance only; automatic file modification and service restarts remain disabled.
- Refuse forced image deletion and adoption of auto-removing containers. Retain the stopped original after successful adoption as a recovery backup.
- Validate managed service names, preserve explicitly selected Compose files, and check new networks against directly connected host subnets.
- Add deterministic regression coverage. This version's validation used command runners and fixtures rather than a live Podman installation.
- Route-table-aware network overlap checks and a dedicated retained-backup management UI remain unimplemented.

## [1.3.0] - 2026-08-31

- Add schema-versioned managed workload replay preserving mounts, environment, commands, lifecycle settings and port ranges.
- Block adoption when the source uses unsupported settings, and verify managed changes with structured rollback reporting.
- Add node-scoped registry reconciliation and explicit handling of ambiguous workload ownership.
- Fail closed during safety-critical port discovery while keeping observation views tolerant of partial failures.
- Limit Compose and Quadlet port editing to discovery and generated guidance.
- Validate network names, address families, gateways and subnet overlap; refuse network removal that would forcibly detach containers.
- Escape untrusted UI data, mask secret-like environment previews and make WebKit sandbox disabling opt-in.
- Preserve IPv6 and port ranges across discovery and replay, and retain ownership labels for upgraded legacy specs.
- Add deterministic tests, CI vet and race checks, and a pinned Wails build.
- Align release identity, package metadata, module path and licensing with Podder.

## [1.2.0] - 2026-08-30

- Add structured published-port discovery with TCP/UDP protocols, bind addresses and port ranges.
- Add container port displays and a Ports tab combining container mappings with Linux host listening sockets.
- Add conflict detection, exposure warnings, free-port suggestions and structured multi-port container creation.
- Add an optional read-only external port registry with live reconciliation and reservation-aware validation.
- Detect Compose, Quadlet/systemd, Pod, Podder and ad-hoc workload origins.
- Add persistent Podder-managed workload specifications and declarative deployment.
- Add transactional managed port changes with verification and rollback.
- Add workload adoption with inspect-to-spec capture, blockers and a review preview.
- Introduce Compose/Quadlet discovery and port-editing integration; later versions restrict these flows to guidance only.
- Add a network browser with IPAM details, container address assignments, network creation and removal.

## [1.1.2] - 2026-07-15

- Update release and packaging metadata to 1.1.2.
- Configure GitHub Actions to publish the Linux binary for the v1.1.2 release tag.

## [0.1.1] - 2026-07-13

- Prefer Podman-native compose execution before falling back to docker-compose.
- Check the rootless Podman API socket and attempt to start podman.socket when compose needs it.
- Add native host-folder and host-image-file selection for mounts.
- Make dashboard summary cards open the corresponding container and image views.

## [1.0.0] - 2026-07-11

- Introduce the Go and Wails v3 desktop application with an HTML/CSS/JavaScript interface.
- Add host metrics, container listings and lifecycle controls, image management and live logs.
- Add a Run Container form for images, names, ports and command arguments.
- Add notifications for operation progress and failures.
- Add compose command handling and the pod/podder CLI entry points.
- Add parsing tests and development/build configuration for Go, Wails and Node.js.
