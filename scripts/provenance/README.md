# Public release provenance

The release workflow signs the tested Podder binary with the pinned
`@actions/attest` toolkit and Sigstore's public-good instance. Fulcio issues the
GitHub Actions identity certificate and Rekor records the attestation in its
public transparency log. Signing, cryptographic verification, and source-policy
verification must all succeed before assets are uploaded to a draft release.

The portable `podder.sigstore.json` bundle is distributed with `podder` and
`SHA256SUMS`. The helper uses `skipWrite: true`, so verification does not depend
on GitHub's attestation-storage API and the workflow needs no
`attestations: write` permission. Public Rekor publication still occurs.

Dependencies and overrides are locked in `package-lock.json`. Install them with
`npm ci --ignore-scripts --prefix scripts/provenance`; the workflow also requires
`npm audit --audit-level=low --prefix scripts/provenance` to pass.

## Release identity

Application source and signing policy must be the same stable, annotated,
SSH-signed tag and commit on `main`. The workflow verifies that tag against
`.github/release-signers` before building and again before signing. The signed
SLSA v1 statement binds:

- The repository name, numeric repository ID, owner ID and hosted runner.
- The `.github/workflows/build.yml` workflow, stable tag and exact commit.
- The separately verified annotated source tag-object ID.
- The binary SHA-256 and exact signer run/attempt.

Repository recreation changes its numeric ID even when its name stays the same.
The workflow supplies `EXPECTED_REPOSITORY_ID` from `${{ github.repository_id }}`
and checks the authenticated OIDC seed against it. Consumers must independently
obtain and pin the intended repository instance's ID; the unverified bundle is
never a source of expected identity. The owner remains pinned to `Eowerd24` and
numeric owner ID `166849211`. A repository transfer requires a reviewed policy
change.

## Signing

Signing runs only in GitHub Actions after signed-tag verification and all build
gates pass. The workflow supplies these values from trusted execution metadata:

| Variable | Meaning |
| --- | --- |
| `SOURCE_REPO` | Exactly `Eowerd24/podder` |
| `EXPECTED_REPOSITORY_ID` | Current `${{ github.repository_id }}` |
| `REPOSITORY_VISIBILITY` | `${{ github.event.repository.visibility }}`, required to be `public` |
| `SOURCE_REF` | Stable application tag, such as `refs/tags/v1.5.6` |
| `SOURCE_SHA` | Verified full source commit, equal to `GITHUB_SHA` |
| `SOURCE_TAG_OBJECT` | Verified annotated source tag-object ID |
| `POLICY_WORKFLOW` | `.github/workflows/build.yml` |
| `GITHUB_SHA`, `GITHUB_REF` | The same source commit and tag |
| `GITHUB_EVENT_NAME` | `push` |
| `GITHUB_RUN_ID`, `GITHUB_RUN_ATTEMPT` | Actual signer run and attempt |
| `GITHUB_ACTIONS` | `true` |

The signing command requires `id-token: write`. It rejects a private repository
before publishing anything to Rekor. The output bundle path must not exist.

```sh
node scripts/provenance/sign.mjs bin/podder bin/podder.sigstore.json
```

## Consumer verification

Download the binary and bundle from the intended release. Obtain the expected
repository ID, tag, commit, tag-object ID, and signer run/attempt from
independently checked repository metadata and the SSH-signed Git tag. Verify
that tag against the reviewed `.github/release-signers` trust file. Do not
populate expected values from an unverified bundle or verification summary.

Use the verifier from the reviewed release checkout. Replace every placeholder
below with that release's independently checked identity:

```sh
export SOURCE_REPO=Eowerd24/podder
export EXPECTED_REPOSITORY_ID=EXPECTED_NUMERIC_REPOSITORY_ID
export POLICY_WORKFLOW=.github/workflows/build.yml
export SOURCE_REF=refs/tags/v1.5.6
export SOURCE_SHA=EXPECTED_FULL_SOURCE_COMMIT
export SOURCE_TAG_OBJECT=EXPECTED_ANNOTATED_TAG_OBJECT
export GITHUB_SHA="$SOURCE_SHA"
export GITHUB_REF="$SOURCE_REF"
export GITHUB_EVENT_NAME=push
export GITHUB_RUN_ID=EXPECTED_SIGNER_RUN_ID
export GITHUB_RUN_ATTEMPT=EXPECTED_SIGNER_RUN_ATTEMPT

gh attestation verify ./podder --bundle ./podder.sigstore.json \
  --repo "$SOURCE_REPO" --deny-self-hosted-runners \
  --predicate-type https://slsa.dev/provenance/v1 \
  --source-digest "$GITHUB_SHA" --source-ref "$GITHUB_REF" \
  --signer-digest "$GITHUB_SHA" \
  --signer-workflow "$SOURCE_REPO/$POLICY_WORKFLOW" \
  --format json > verified.json && \
node scripts/provenance/verify.mjs ./podder verified.json \
  "$SOURCE_REF" "$SOURCE_SHA" "$SOURCE_TAG_OBJECT"
```

The first command verifies the signature, certificate chain, artifact digest,
workflow identity and transparency evidence using normal Sigstore trust roots.
Trust-root metadata may require network access unless already cached. Do not
supply `--no-public-good`, a custom trust root, or options that skip verification.

The second command reads only the successful command's unfiltered
`verificationResult`, requires public visibility and a verified `Tlog` witness,
and checks the exact repository instance, owner, source tag object, workflow,
commit and run/attempt. It independently hashes the local binary. A fabricated
JSON summary is not cryptographic evidence: run the commands in this order,
with the first command succeeding. Checksums alone do not establish identity.

## Tests

```sh
npm test --prefix scripts/provenance
```

Synthetic summaries test semantic acceptance and rejection of changed source,
repository identities, signing context, artifact hashes and transparency
witnesses. They also check recreated repository IDs and CLI hashing. These tests
do not exercise live OIDC issuance, certificate validation, or Rekor inclusion;
those checks run in the release workflow.

Primary references:
[toolkit signing and storage API](https://github.com/actions/toolkit/tree/main/packages/attest),
[GitHub CLI verification policy](https://cli.github.com/manual/gh_attestation_verify),
[Sigstore verification result](https://github.com/sigstore/sigstore-go/blob/v1.3.0/pkg/verify/signed_entity.go).
