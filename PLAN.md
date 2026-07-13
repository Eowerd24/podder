# PLAN: Native Host Path Pickers And Dashboard Filter Navigation

## Objective
Improve Podder's container workflow by letting users choose host folders or image files from a native file explorer when preparing a bind mount, and make the dashboard stat cards route cleanly into the Images or Containers views with the appropriate filters already selected.

## Scope & Impact Analysis
- **Files to modify**:
  - `podman.go`: add native picker support for host folders and image files; extend container run support with optional bind-mount arguments.
  - `podman_test.go`: add regression coverage for run-argument assembly and image-file validation helpers.
  - `frontend/index.html`: expand the run modal with host-path controls and make the dashboard stat cards explicit interactive controls.
  - `frontend/src/main.js`: wire picker actions, bind-mount submission, and filtered dashboard navigation state.
  - `frontend/public/style.css`: style the new picker controls, filter subtitles, and clickable stat-card buttons.
  - `docs/project-context.md`: record the native file-picker decision for filesystem-backed container workflows.
  - `CHANGELOG.md`: capture the user-facing workflow changes.
- **Runtime impact**:
  - Users can pick a host folder or a host image file without typing absolute paths manually.
  - Running or stopped container cards on the dashboard open the Containers tab with a visible filtered state.
  - Existing image pull/build/compose workflows remain unchanged.

## Implementation Approach
1. Add backend helpers for:
   - validating supported image-file selections
   - building `podman run` arguments with an optional bind mount
   - opening native dialogs for either a host folder or a host image file
2. Expand the Run Container modal with:
   - a read-only selected host path field
   - separate picker buttons for folders and image files
   - a container mount target path field
   - a read-only toggle for the mount
3. Route run submissions through Wails runtime `Call.ByName(...)` for the new picker flow so the frontend does not depend on regenerated numeric binding IDs inside this workspace.
4. Make dashboard stat cards explicit button-like controls and add a visible subtitle on the Containers view so the selected filter is obvious after navigation.

## Testing Strategy
- Run Go unit tests for the new pure helper logic if a Go toolchain is available.
- Manually verify:
  - selecting a folder populates the run modal and mounts it into a new container
  - selecting an image file populates the run modal and mounts it into a new container
  - invalid non-image file selections are rejected with a clear error
  - clicking dashboard cards opens All Containers, Running, Stopped, or Images as appropriate
- Confirm no regressions in image listing, container listing, compose actions, and image build selection.

## Rollback Plan
- Revert the run-modal picker additions in the frontend.
- Revert the optional bind-mount and picker logic in `podman.go`.
- Remove the docs and changelog entries describing the feature.
