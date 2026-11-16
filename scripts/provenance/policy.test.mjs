import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { BUILD_TYPE, PREDICATE_TYPE, REPOSITORY_URI, contextFromEnvironment,
  completePredicate, verifyVerifiedResult } from './policy.mjs';

// Synthetic gh verification summaries exercise semantic policy, not signature,
// certificate-chain, log inclusion, or Git tag cryptographic verification.
const binary = Buffer.from('synthetic Podder artifact fixture');
const digest = createHash('sha256').update(binary).digest('hex');
const env = {
  SOURCE_REPO: 'Eowerd24/podder', EXPECTED_REPOSITORY_ID: '9876543210',
  SOURCE_REF: 'refs/tags/v1.5.6', SOURCE_SHA: 'a'.repeat(40),
  SOURCE_TAG_OBJECT: 'b'.repeat(40),
  GITHUB_SHA: 'a'.repeat(40), GITHUB_REF: 'refs/tags/v1.5.6',
  POLICY_WORKFLOW: '.github/workflows/build.yml',
  GITHUB_RUN_ID: '123456', GITHUB_RUN_ATTEMPT: '2', GITHUB_EVENT_NAME: 'push',
};
const context = contextFromEnvironment(env);

function seed(c = context) {
  return { type: PREDICATE_TYPE, params: {
    buildDefinition: {
      buildType: BUILD_TYPE,
      externalParameters: { workflow: { repository: REPOSITORY_URI, path: c.workflow, ref: c.ref } },
      internalParameters: { github: { event_name: c.event, repository_id: c.repositoryID,
        repository_owner_id: '166849211', runner_environment: 'github-hosted' } },
      resolvedDependencies: [{ uri: `git+${REPOSITORY_URI}@${c.ref}`, digest: { gitCommit: c.sha } }],
    },
    runDetails: { builder: { id: c.workflowURI }, metadata: { invocationId: c.invocationURI } },
  } };
}

function fixture(c = context) {
  return [{ verificationResult: {
    signature: { certificate: {
      issuer: 'https://token.actions.githubusercontent.com', sourceRepositoryURI: REPOSITORY_URI,
      sourceRepositoryIdentifier: c.repositoryID, sourceRepositoryOwnerIdentifier: '166849211',
      sourceRepositoryOwnerURI: 'https://github.com/Eowerd24', sourceRepositoryVisibilityAtSigning: 'public',
      sourceRepositoryDigest: c.sha, sourceRepositoryRef: c.ref,
      buildConfigDigest: c.sha, buildConfigURI: c.workflowURI,
      buildSignerDigest: c.sha, buildSignerURI: c.workflowURI,
      subjectAlternativeName: c.workflowURI, runnerEnvironment: 'github-hosted',
      runInvocationURI: c.invocationURI, buildTrigger: c.event,
    } },
    verifiedTimestamps: [{ type: 'Tlog', uri: 'https://rekor.sigstore.dev', timestamp: '2026-11-16T04:00:00Z' }],
    statement: { _type: 'https://in-toto.io/Statement/v1', predicateType: PREDICATE_TYPE,
      subject: [{ name: 'podder', digest: { sha256: digest } }],
      predicate: completePredicate(seed(c), c) },
  } }];
}

test('accepts public tag provenance bound to the expected repository instance', () => {
  const result = verifyVerifiedResult(fixture(), digest, context);
  assert.equal(result.semanticPolicyVerified, true);
  assert.equal(result.repositoryID, env.EXPECTED_REPOSITORY_ID);
});

test('supports a recreated repository only when its independently expected ID is supplied', () => {
  const recreated = contextFromEnvironment({ ...env, EXPECTED_REPOSITORY_ID: '8765432109',
    GITHUB_REPOSITORY_ID: '8765432109' });
  assert.equal(verifyVerifiedResult(fixture(recreated), digest, recreated).semanticPolicyVerified, true);
  assert.throws(() => verifyVerifiedResult(fixture(recreated), digest, context));
});

for (const [name, change] of [
  ['application commit', r => { r.statement.predicate.buildDefinition.externalParameters.source.commit = 'c'.repeat(40); }],
  ['application tag object', r => { r.statement.predicate.buildDefinition.externalParameters.source.tagObject = 'c'.repeat(40); }],
  ['source dependency', r => { r.statement.predicate.buildDefinition.resolvedDependencies[1].digest.gitCommit = 'c'.repeat(40); }],
  ['policy dependency', r => { r.statement.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit = 'c'.repeat(40); }],
  ['policy workflow', r => { r.statement.predicate.buildDefinition.externalParameters.workflow.path = '.github/workflows/other.yml'; }],
  ['source repository', r => { r.statement.predicate.buildDefinition.externalParameters.source.repository = 'https://github.com/attacker/podder'; }],
  ['certificate signer', r => { r.signature.certificate.buildSignerURI = 'https://github.com/attacker/podder/.github/workflows/build.yml@refs/heads/main'; }],
  ['certificate policy digest', r => { r.signature.certificate.sourceRepositoryDigest = 'c'.repeat(40); }],
  ['repository name takeover', r => { r.signature.certificate.sourceRepositoryIdentifier = '123'; }],
  ['repository owner takeover', r => { r.signature.certificate.sourceRepositoryOwnerIdentifier = '123'; }],
  ['signed repository ID', r => { r.statement.predicate.buildDefinition.internalParameters.github.repository_id = '123'; }],
  ['private signing context', r => { r.signature.certificate.sourceRepositoryVisibilityAtSigning = 'private'; }],
  ['self-hosted runner', r => { r.signature.certificate.runnerEnvironment = 'self-hosted'; }],
  ['wrong run', r => { r.signature.certificate.runInvocationURI = `${REPOSITORY_URI}/actions/runs/999/attempts/1`; }],
  ['wrong signed invocation', r => { r.statement.predicate.runDetails.metadata.invocationId = `${REPOSITORY_URI}/actions/runs/999/attempts/1`; }],
  ['wrong artifact hash', r => { r.statement.subject[0].digest.sha256 = '0'.repeat(64); }],
  ['ambiguous subjects', r => { r.statement.subject.push(structuredClone(r.statement.subject[0])); }],
  ['missing transparency witness', r => { r.verifiedTimestamps = []; }],
  ['private timestamp witness', r => { r.verifiedTimestamps[0].type = 'TimestampAuthority'; }],
  ['invalid timestamp', r => { r.verifiedTimestamps[0].timestamp = 'not-a-time'; }],
  ['missing predicate', r => { delete r.statement.predicate; }],
]) {
  test(`rejects tampered ${name}`, () => {
    const results = fixture(); change(results[0].verificationResult);
    assert.throws(() => verifyVerifiedResult(results, digest, context));
  });
}

test('rejects raw unauthenticated bundle body and multiple verified results', () => {
  const results = fixture();
  assert.throws(() => verifyVerifiedResult([{ attestation: { statement: results[0].verificationResult.statement } }], digest, context));
  assert.throws(() => verifyVerifiedResult([...results, ...results], digest, context));
});

for (const [name, changed] of [
  ['missing repository ID', { EXPECTED_REPOSITORY_ID: undefined }],
  ['invalid repository ID', { EXPECTED_REPOSITORY_ID: '0' }],
  ['conflicting workflow repository ID', { GITHUB_REPOSITORY_ID: '123' }],
  ['unexpected owner', { GITHUB_REPOSITORY_OWNER_ID: '123' }],
  ['different source commit', { SOURCE_SHA: 'c'.repeat(40) }],
  ['different source tag', { SOURCE_REF: 'refs/tags/v1.5.7' }],
  ['recovery workflow', { POLICY_WORKFLOW: '.github/workflows/release-recovery.yml' }],
  ['recovery tag', { GITHUB_REF: 'refs/tags/v1.5.6-recovery.1' }],
  ['manual dispatch', { GITHUB_EVENT_NAME: 'workflow_dispatch' }],
]) {
  test(`rejects untrusted context: ${name}`, () => {
    assert.throws(() => contextFromEnvironment({ ...env, ...changed }));
  });
}

test('signing rejects OIDC seed identities that disagree with trusted workflow metadata', () => {
  for (const alter of [
    s => { s.params.runDetails.builder.id = `${REPOSITORY_URI}/attacker.yml`; },
    s => { s.params.buildDefinition.internalParameters.github.repository_id = '123'; },
    s => { s.params.buildDefinition.internalParameters.github.repository_owner_id = '123'; },
    s => { s.params.buildDefinition.resolvedDependencies[0].digest.gitCommit = 'c'.repeat(40); },
  ]) {
    const altered = seed(); alter(altered);
    assert.throws(() => completePredicate(altered, context));
  }
});

test('CLI recomputes artifact digest and exits nonzero after a real file change', async () => {
  const root = await mkdtemp(join(tmpdir(), 'podder-provenance-semantics-'));
  try {
    const binaryPath = join(root, 'podder'); const resultsPath = join(root, 'verified.json');
    await writeFile(binaryPath, binary); await writeFile(resultsPath, JSON.stringify(fixture()));
    const args = [fileURLToPath(new URL('./verify.mjs', import.meta.url)), binaryPath, resultsPath,
      env.SOURCE_REF, env.SOURCE_SHA, env.SOURCE_TAG_OBJECT];
    const childEnv = { ...process.env, ...env };
    for (const key of ['GITHUB_WORKFLOW_REF', 'GITHUB_WORKFLOW_SHA', 'GITHUB_REPOSITORY',
      'GITHUB_REPOSITORY_ID', 'GITHUB_REPOSITORY_OWNER_ID']) delete childEnv[key];
    assert.equal(spawnSync(process.execPath, args, { env: childEnv }).status, 0);
    await writeFile(binaryPath, 'substituted artifact');
    assert.notEqual(spawnSync(process.execPath, args, { env: childEnv }).status, 0);
  } finally { await rm(root, { recursive: true, force: true }); }
});
