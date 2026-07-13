# PLAN: Podman Socket Preflight For Compose Launches

## Objective
Reduce first-run compose failures by having Podder detect and start the rootless `podman.socket` before running compose commands that depend on the Podman Docker-compatible API socket.

## Scope & Impact Analysis
- **Files to modify**:
  - `main.go`: add compose-provider metadata and a preflight that checks and starts `podman.socket` when Podder is about to use a provider that talks to the Podman API socket.
  - `README.md`: document the automatic socket start behavior and the remaining host requirement for persistent availability across reboots.
  - `docs/project-context.md`: record the launcher preflight decision as an ADR update.
  - `CHANGELOG.md`: note the user-facing compose startup improvement.
- **Runtime impact**:
  - `pod up` / `podder up` and `down` become more robust on systems where Podman is installed but the user socket is not already active.
  - No background mutation occurs at package install time; Podder only attempts to start the socket at the moment it is needed.

## Implementation Approach
1. Refactor compose provider selection into metadata describing:
   - the command to run
   - whether it relies on the Podman API socket
2. Add a preflight helper that:
   - derives the expected socket path from `XDG_RUNTIME_DIR` or `/run/user/<uid>/podman/podman.sock`
   - skips work if the socket already exists
   - otherwise runs `systemctl --user start podman.socket`
   - rechecks the socket and returns a clear actionable error if startup fails
3. Keep install-time behavior conservative:
   - do not auto-enable lingering or mutate per-user systemd state from package scripts
   - document `loginctl enable-linger` as an optional host-level persistence step
4. Add unit tests for socket path resolution and provider selection behavior where feasible without a live Podman daemon.

## Testing Strategy
- Run Go tests for helper logic in `main.go`.
- Manually verify the compose flow in three scenarios:
  - socket already active
  - socket inactive but user systemd available
  - socket inactive and user systemd unavailable, expecting a clear error
- Confirm the existing GUI entrypoint behavior is unchanged.

## Rollback Plan
- Revert the `main.go` compose preflight changes.
- Remove the README/docs/changelog entries describing socket auto-start behavior.
