# PLAN: Patch Version Bump To 0.1.1

## Objective
Bump Podder's application version by `0.0.1`, normalize inconsistent version metadata across packaging files, update the README and changelog to match, and create a fresh local commit on the current feature branch.

## Scope & Impact Analysis
- **Files to modify**:
  - `build/config.yml`: align the app version source with the new release version.
  - Platform packaging/version manifests that currently reference `0.1.0` or `0.0.1`:
    - `build/linux/nfpm/nfpm.yaml`
    - `build/darwin/Info.plist`
    - `build/darwin/Info.dev.plist`
    - `build/ios/Info.plist`
    - `build/ios/Info.dev.plist`
    - `build/windows/info.json`
    - `build/windows/nsis/wails_tools.nsh`
    - `build/windows/wails.exe.manifest`
    - `build/windows/msix/app_manifest.xml`
    - `build/windows/msix/template.xml`
  - `README.md`: mention the current release version explicitly so install/build docs reflect the bump.
  - `CHANGELOG.md`: convert the current unreleased entries into a `0.1.1` release entry.
- **Runtime impact**:
  - No behavioral change to Podder itself.
  - Build outputs and packaged binaries will report `0.1.1` consistently across supported platforms.

## Implementation Approach
1. Treat the existing effective release baseline as `0.1.0` and bump it to `0.1.1`.
2. Correct the outlier in `build/config.yml` from `0.0.1` to `0.1.1` so the central build metadata matches the platform manifests.
3. Update README release wording so the current version is visible in the install/build guidance.
4. Roll the current unreleased changes into a dated `0.1.1` changelog section.
5. Commit the version/documentation changes locally on the active branch.

## Testing Strategy
- Verify all targeted manifest files now reference `0.1.1` consistently.
- Re-scan the repo for stale `0.1.0` / `0.0.1` version strings in Podder-owned release metadata.
- Confirm the branch is clean after the new commit.

## Rollback Plan
- Revert the manifest and documentation edits.
- Remove the local version-bump commit if needed.
