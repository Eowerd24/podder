#!/usr/bin/env node
import { writeFile } from 'node:fs/promises';
import toolkit from '@actions/attest';
import { binaryDigest, completePredicate, contextFromEnvironment, equal, PREDICATE_TYPE } from './policy.mjs';

async function main() {
  if (process.argv.length !== 4) throw new Error('Usage: node sign.mjs BINARY OUTPUT_BUNDLE');
  equal(process.env.GITHUB_ACTIONS, 'true', 'GitHub Actions execution');
  equal(process.env.REPOSITORY_VISIBILITY, 'public', 'signing repository visibility');
  const context = contextFromEnvironment(process.env);
  const digest = await binaryDigest(process.argv[2]);
  const seed = await toolkit.buildSLSAProvenancePredicate();
  const predicate = completePredicate(seed, context);
  // Public Fulcio signing publishes to Rekor. Keep the portable bundle as a
  // release asset; GitHub attestation API storage is not required for verification.
  const signed = await toolkit.attest({
    subjects: [{ name: 'podder', digest: { sha256: digest } }],
    predicateType: PREDICATE_TYPE,
    predicate,
    token: '',
    sigstore: 'public-good',
    skipWrite: true,
  });
  await writeFile(process.argv[3], `${JSON.stringify(signed.bundle)}\n`, { flag: 'wx', mode: 0o600 });
  process.stdout.write(`${JSON.stringify({ bundle: process.argv[3], sha256: digest, source: context.source,
    policy: { workflow: context.workflow, ref: context.ref, commit: context.sha } })}\n`);
}

main().catch(error => { console.error(`Public provenance signing failed: ${error.message}`); process.exitCode = 1; });
