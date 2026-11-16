# Maintainer release setup

Configure the following controls on the new public GitHub repository before its first release. These files describe the intended policy; they do not apply or verify remote settings. A recreated repository has a new numeric repository ID and needs its own rules, environment, and Actions configuration.

## Repository controls

1. Protect `main` against deletion and force pushes. Require pull requests, successful **Test & Build** checks from GitHub Actions, resolved conversations, signed commits, and at least one independent approving review. Dismiss stale approvals and require approval after the latest reviewable push. Add a trusted reviewer or team as CODEOWNER for `.github/`, `scripts/provenance/`, `scripts/verify-release.py`, and this file. Verify that the selected rules are active and avoid routine administrator bypass.
2. Protect `v*` tags against updates and deletion. Restrict tag creation to designated release maintainers through a separate creation rule; a creation exception must not bypass update or deletion protections. Signed commits alone do not establish a signed tag.
3. Configure the `release` environment for `v*` tags only. Require an independent reviewer, prevent self-review, and disable administrator bypass. Merely naming an environment in a workflow does not protect it.
4. Restrict allowed Actions to the full commit SHA pins reviewed in `.github/workflows/build.yml`. Default workflow permissions to read-only, disable Actions approval of pull requests, and grant content-write and OIDC permissions only to the release job that needs them.
5. Enable release immutability before publication. Build and verify release assets first, upload the binary, provenance bundle and checksums to a draft release, verify their digests, then publish them together.
6. Enable private vulnerability reporting and check that the reporting form linked from [SECURITY.md](SECURITY.md) is available.

## Signing identities

Verify each maintainer's SSH signing public-key fingerprint through an independent channel before adding it to `.github/release-signers` in a reviewed pull request. That file contains public trust anchors; keep private signing keys outside the repository and Actions. Review key additions and replacements with the same care as release workflow changes.

The release workflow must verify the annotated tag's SSH signature against this allow-list, bind the tag to the exact checked-out commit and `main` history, and reject mismatched versions or untrusted signatures. Confirm that unsigned and untrusted tags are rejected before granting publication access.

## First release from the clean history

Push only the reviewed clean history. Do not import the previous repository's branches, tags, audit commits, release assets, or one-time recovery workflow. Confirm the new repository's numeric ID and use it when independently checking provenance; the repository name alone does not distinguish a recreated repository.

The source snapshot retains version `1.5.5`. Prepare the next release with a version greater than `1.5.5`, such as `1.5.6`, updating every packaging and application version consistently before tagging. Use the approved final commit and release date. Never reuse an already published package version.

After review and passing checks, create an annotated SSH-signed tag locally, using your private signing key's actual path:

```sh
git -c gpg.format=ssh \
  -c user.signingkey=/path/to/private-signing-key \
  tag -s v1.5.6 APPROVED_RELEASE_COMMIT
```

The next release uses public Sigstore provenance. Check the repository ID, workflow, signed source tag, commit, tag-object ID, run identity and binary digest before publication. Follow [the provenance instructions](scripts/provenance/README.md) for signing and consumer verification. Obtain expected identities independently rather than copying them from an unverified bundle.

Passing CI, a valid signature and immutable assets do not replace independent source review. If a configured protection cannot be enforced, resolve that gap before treating the release process as protected.
