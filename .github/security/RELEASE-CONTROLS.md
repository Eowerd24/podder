# Release controls

[Maintainer release setup](../../SECURITY-SETUP.md) describes the controls to configure on the new public repository. Repository files and workflow declarations do not establish that remote settings are active.

Before the first release, verify that:

- `main` requires passing checks and independent review, with deletion and force pushes blocked.
- Release maintainers alone can create `v*` tags, and existing tags cannot be updated or deleted. Annotated release tags pass the SSH signature gate using the public keys in `.github/release-signers`.
- The `release` environment permits only version tags, requires independent approval, prevents self-review and administrator bypass, and has no branch wildcard.
- Actions are restricted to reviewed full commit SHA pins. Default permissions are read-only; release content-write and OIDC permissions remain scoped to the release job.
- Unit, race, frontend, reachable-vulnerability and native-build checks pass before signing or publishing.
- Public Sigstore provenance binds the binary digest to the expected repository ID, workflow, source tag and commit, and signing run. Consumer verification follows [the provenance instructions](../../scripts/provenance/README.md).
- Release immutability is enabled. The verified binary, provenance bundle and checksums are uploaded to a draft and published together.

Start from the reviewed clean history and configure the new repository's settings and numeric identity. Do not restore old audit history, tags or recovery workflows. Prepare the next version above `1.5.5`; the imported source snapshot's version is not itself a new release.
