# PLAN: Verify Container Filters and Folder Selection

## Scope & Impact Analysis
- Trace dashboard filter-square click handlers through tab navigation and container loading.
- Trace compose/build folder selection from the frontend bridge into the Go service and back.
- Fix only the broken interaction paths and add focused regression coverage where the existing test setup permits.
- Runtime impact is limited to filtered container navigation and whole-folder selection workflows.

## Files To Be Created/Modified
- `PLAN.md`: replace the stale task plan with this investigation plan.
- `frontend/src/main.js`: adjust filter or folder-selection behavior if the source confirms a frontend defect.
- `frontend/index.html`: adjust event wiring only if required by the investigation.
- `podman.go`: adjust native folder selection or compose/build path handling only if required.
- `main_test.go`, `podman_test.go`, or focused frontend tests: add regression coverage where practical.
- `docs/project-context.md`: record any architectural decision introduced by the fix.
- `CHANGELOG.md`: record user-visible behavior changes.

## Testing Strategy
- Run the frontend build and Go formatting/tests.
- Launch the development app and exercise each dashboard filter and folder-selection action.
- Verify the selected container view and selected directory are reflected in the resulting workflow.

## Rollback Plan
- Revert only the files changed for this investigation, preserving unrelated worktree changes.
- If runtime validation exposes an environment-only issue, retain the diagnosis and avoid speculative code changes.
