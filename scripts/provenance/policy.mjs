import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { isDeepStrictEqual } from 'node:util';

export const REPOSITORY = 'Eowerd24/podder';
export const REPOSITORY_URI = `https://github.com/${REPOSITORY}`;
export const OWNER_ID = '166849211';
export const PREDICATE_TYPE = 'https://slsa.dev/provenance/v1';
export const BUILD_TYPE = 'https://actions.github.io/buildtypes/workflow/v1';

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

function match(value, pattern, label) {
  requireValue(typeof value === 'string' && pattern.test(value), `Invalid ${label}`);
  return value;
}

export function equal(actual, expected, label) {
  requireValue(isDeepStrictEqual(actual, expected), `Unexpected ${label}`);
}

/** Values here are verification policy supplied by the trusted workflow/caller. */
export function contextFromEnvironment(env, sourceOverride) {
  equal(env.SOURCE_REPO, REPOSITORY, 'source repository');
  equal(env.GITHUB_SERVER_URL ?? 'https://github.com', 'https://github.com', 'GitHub server');
  if (env.GITHUB_REPOSITORY !== undefined) equal(env.GITHUB_REPOSITORY, REPOSITORY, 'workflow repository');
  // Supplied by trusted workflow metadata, or independently obtained by consumers.
  // Never derive this expected identity from the unverified bundle.
  const repositoryID = match(env.EXPECTED_REPOSITORY_ID, /^[1-9]\d*$/, 'expected repository ID');
  const source = {
    repository: REPOSITORY_URI,
    ref: match(sourceOverride?.ref ?? env.SOURCE_REF, /^refs\/tags\/v\d+\.\d+\.\d+$/, 'source ref'),
    commit: match(sourceOverride?.sha ?? env.SOURCE_SHA, /^[0-9a-f]{40}$/, 'source commit'),
    tagObject: match(sourceOverride?.tagObject ?? env.SOURCE_TAG_OBJECT, /^[0-9a-f]{40}$/, 'source tag object'),
  };
  const workflow = env.POLICY_WORKFLOW;
  equal(workflow, '.github/workflows/build.yml', 'policy workflow');
  const sha = match(env.GITHUB_SHA, /^[0-9a-f]{40}$/, 'policy commit');
  const ref = match(env.GITHUB_REF, /^refs\/tags\/v\d+\.\d+\.\d+$/, 'policy ref');
  const event = env.GITHUB_EVENT_NAME;
  equal(event, 'push', 'release build event');
  const runID = match(env.GITHUB_RUN_ID, /^[1-9]\d*$/, 'workflow run ID');
  const runAttempt = match(env.GITHUB_RUN_ATTEMPT, /^[1-9]\d*$/, 'workflow run attempt');
  equal(ref, source.ref, 'source/policy ref');
  equal(sha, source.commit, 'source/policy commit');
  const workflowRef = `${REPOSITORY}/${workflow}@${ref}`;
  if (env.GITHUB_WORKFLOW_REF !== undefined) equal(env.GITHUB_WORKFLOW_REF, workflowRef, 'workflow identity');
  if (env.GITHUB_WORKFLOW_SHA !== undefined) equal(env.GITHUB_WORKFLOW_SHA, sha, 'workflow policy digest');
  if (env.GITHUB_REPOSITORY_ID !== undefined) equal(env.GITHUB_REPOSITORY_ID, repositoryID, 'repository ID');
  if (env.GITHUB_REPOSITORY_OWNER_ID !== undefined) equal(env.GITHUB_REPOSITORY_OWNER_ID, OWNER_ID, 'owner ID');
  return {
    source, repositoryID, workflow, sha, ref, event, runID, runAttempt,
    workflowURI: `https://github.com/${workflowRef}`,
    invocationURI: `${REPOSITORY_URI}/actions/runs/${runID}/attempts/${runAttempt}`,
  };
}

export async function binaryDigest(binaryPath) {
  const bytes = await readFile(binaryPath);
  requireValue(bytes.length > 0, 'Binary must not be empty');
  return createHash('sha256').update(bytes).digest('hex');
}

function workflowParameter(context) {
  return { repository: REPOSITORY_URI, path: context.workflow, ref: context.ref };
}

function githubParameters(context) {
  return {
    event_name: context.event,
    repository_id: context.repositoryID,
    repository_owner_id: OWNER_ID,
    runner_environment: 'github-hosted',
  };
}

function dependencies(context) {
  return [
    { name: 'workflow-policy', uri: `git+${REPOSITORY_URI}@${context.ref}`, digest: { gitCommit: context.sha } },
    { name: 'podder-source', uri: `git+${REPOSITORY_URI}@${context.source.ref}`, digest: { gitCommit: context.source.commit } },
  ];
}

/** Validate authenticated OIDC-derived seed metadata before adding source inputs. */
export function completePredicate(seed, context) {
  equal(seed?.type, PREDICATE_TYPE, 'seed predicate type');
  const predicate = structuredClone(seed.params);
  equal(predicate?.buildDefinition?.buildType, BUILD_TYPE, 'seed build type');
  equal(predicate.buildDefinition.externalParameters, { workflow: workflowParameter(context) }, 'seed workflow');
  equal(predicate.buildDefinition.internalParameters, { github: githubParameters(context) }, 'seed GitHub claims');
  equal(predicate.buildDefinition.resolvedDependencies,
    [{ uri: `git+${REPOSITORY_URI}@${context.ref}`, digest: { gitCommit: context.sha } }], 'seed policy dependency');
  equal(predicate.runDetails, {
    builder: { id: context.workflowURI }, metadata: { invocationId: context.invocationURI },
  }, 'seed invocation');
  predicate.buildDefinition.externalParameters.source = structuredClone(context.source);
  predicate.buildDefinition.resolvedDependencies = dependencies(context);
  return predicate;
}

/**
 * Additional semantic policy ONLY. The input must be unfiltered successful
 * `gh attestation verify --format json` output, generated by the trusted caller
 * with the mandatory cryptographic flags documented in README.md.
 * A JSON file alone is not evidence of successful signature verification.
 */
export function verifyVerifiedResult(results, digest, context) {
  requireValue(Array.isArray(results) && results.length === 1, 'Require exactly one cryptographically verified result');
  const result = results[0]?.verificationResult;
  requireValue(result && typeof result === 'object', 'Missing verificationResult; raw bundle statements are not accepted');
  const certificate = result.signature?.certificate;
  requireValue(certificate && typeof certificate === 'object', 'Missing verified certificate summary');
  const requiredCertificate = {
    issuer: 'https://token.actions.githubusercontent.com',
    sourceRepositoryURI: REPOSITORY_URI,
    sourceRepositoryIdentifier: context.repositoryID,
    sourceRepositoryOwnerIdentifier: OWNER_ID,
    sourceRepositoryOwnerURI: 'https://github.com/Eowerd24',
    sourceRepositoryVisibilityAtSigning: 'public',
    sourceRepositoryDigest: context.sha,
    sourceRepositoryRef: context.ref,
    buildConfigDigest: context.sha,
    buildConfigURI: context.workflowURI,
    buildSignerDigest: context.sha,
    buildSignerURI: context.workflowURI,
    subjectAlternativeName: context.workflowURI,
    runnerEnvironment: 'github-hosted',
    runInvocationURI: context.invocationURI,
    buildTrigger: context.event,
  };
  for (const [key, value] of Object.entries(requiredCertificate)) equal(certificate[key], value, `certificate ${key}`);
  const timestamps = result.verifiedTimestamps;
  requireValue(Array.isArray(timestamps) && timestamps.length >= 1, 'Missing verified transparency timestamp');
  for (const timestamp of timestamps) {
    equal(timestamp.type, 'Tlog', 'public transparency witness type');
    requireValue(typeof timestamp.timestamp === 'string' && Number.isFinite(Date.parse(timestamp.timestamp)),
      'Invalid verified timestamp');
  }
  const statement = result.statement;
  requireValue(statement && typeof statement === 'object', 'Missing verified statement');
  // Older gh/Sigstore JSON summaries omit these envelope fields. gh must enforce
  // --predicate-type during cryptographic verification; reject conflicts here.
  if (statement.predicateType !== undefined) equal(statement.predicateType, PREDICATE_TYPE, 'predicate type');
  if (statement._type !== undefined) equal(statement._type, 'https://in-toto.io/Statement/v1', 'statement type');
  equal(statement.subject, [{ name: 'podder', digest: { sha256: digest } }], 'artifact subject and digest');
  const predicate = statement.predicate;
  requireValue(predicate && typeof predicate === 'object', 'Missing signed predicate');
  equal(predicate.buildDefinition, {
    buildType: BUILD_TYPE,
    externalParameters: { workflow: workflowParameter(context), source: context.source },
    internalParameters: { github: githubParameters(context) },
    resolvedDependencies: dependencies(context),
  }, 'signed source and policy definition');
  equal(predicate.runDetails, {
    builder: { id: context.workflowURI }, metadata: { invocationId: context.invocationURI },
  }, 'signed builder and invocation');
  equal(Object.keys(predicate).sort(), ['buildDefinition', 'runDetails'], 'predicate fields');
  return {
    semanticPolicyVerified: true,
    sha256: digest,
    source: context.source,
    repositoryID: context.repositoryID,
    policy: { workflow: context.workflow, ref: context.ref, commit: context.sha },
    invocation: context.invocationURI,
  };
}
