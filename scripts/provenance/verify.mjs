#!/usr/bin/env node
import { readFile } from 'node:fs/promises';
import { binaryDigest, contextFromEnvironment, verifyVerifiedResult } from './policy.mjs';

async function main() {
  if (process.argv.length !== 7) {
    throw new Error('Usage: node verify.mjs BINARY GH_VERIFIED_JSON EXPECTED_SOURCE_REF EXPECTED_SOURCE_SHA EXPECTED_TAG_OBJECT');
  }
  const [binary, verifiedJSON, ref, sha, tagObject] = process.argv.slice(2);
  const context = contextFromEnvironment(process.env, { ref, sha, tagObject });
  const verified = JSON.parse(await readFile(verifiedJSON, 'utf8'));
  const result = verifyVerifiedResult(verified, await binaryDigest(binary), context);
  process.stdout.write(`${JSON.stringify(result)}\n`);
}

main().catch(error => { console.error(`Public provenance policy failed: ${error.message}`); process.exitCode = 1; });
