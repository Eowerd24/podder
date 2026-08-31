// Unit tests for the trust-boundary helpers in trust.js: escapeHtml,
// escapeAttr, jsStringLiteral, jsonToSafeAttr, and withMaskedSecrets.
//
// These functions are the last line of defense between untrusted data
// (container names, image names, labels, provenance, registry YAML,
// process names, network names, ...) and the HTML/JS the app renders.
// Uses Node's built-in test runner (`node --test`) so this suite needs no
// browser, DOM, or extra dependency to run.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
    escapeHtml,
    escapeAttr,
    jsStringLiteral,
    jsonToSafeAttr,
    withMaskedSecrets,
} from './trust.js';

// Payloads an attacker-controlled field (a container name, an image tag, a
// label value, provenance metadata, a line from registry YAML, a process
// name from `ss`, a network name, ...) could plausibly contain.
const MALICIOUS_PAYLOADS = [
    `'`,
    `"`,
    `<`,
    `>`,
    `&`,
    `\\`,
    `line1\nline2`,
    `</script>`,
    `<img src=x onerror=alert(1)>`,
    `" onclick="alert(1)`,
    `'; alert(document.cookie); '`,
    `<script>alert('xss')</script>`,
    `javascript:alert(1)`,
    `\u2028\u2029 line/paragraph separators`,
    `container-name/with"quotes'and<tags>&amps`,
];

test('escapeHtml neutralizes every HTML metacharacter', () => {
    for (const payload of MALICIOUS_PAYLOADS) {
        const out = escapeHtml(payload);
        assert.ok(!out.includes('<'), `expected no raw '<' in escaped output for ${JSON.stringify(payload)}, got ${JSON.stringify(out)}`);
        assert.ok(!out.includes('>'), `expected no raw '>' in escaped output for ${JSON.stringify(payload)}, got ${JSON.stringify(out)}`);
        assert.ok(!out.includes('"'), `expected no raw '"' in escaped output for ${JSON.stringify(payload)}, got ${JSON.stringify(out)}`);
        assert.ok(!out.includes("'"), `expected no raw "'" in escaped output for ${JSON.stringify(payload)}, got ${JSON.stringify(out)}`);
    }
});

test('escapeHtml handles the concrete OWASP-style breakout vectors', () => {
    assert.equal(escapeHtml(`<script>alert('xss')</script>`), '&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;');
    assert.equal(escapeHtml(`<img src=x onerror=alert(1)>`), '&lt;img src=x onerror=alert(1)&gt;');
    assert.equal(escapeHtml(`" onclick="alert(1)`), '&quot; onclick=&quot;alert(1)');
    assert.equal(escapeHtml(`a & b`), 'a &amp; b');
});

test('escapeHtml is null/undefined-safe', () => {
    assert.equal(escapeHtml(null), '');
    assert.equal(escapeHtml(undefined), '');
    assert.equal(escapeHtml(0), '0');
    assert.equal(escapeHtml(false), 'false');
});

test('escapeAttr matches escapeHtml (documented alias for attribute contexts)', () => {
    for (const payload of MALICIOUS_PAYLOADS) {
        assert.equal(escapeAttr(payload), escapeHtml(payload));
    }
});

test('escapeAttr output is safe when substituted into a double-quoted HTML attribute', () => {
    for (const payload of MALICIOUS_PAYLOADS) {
        const html = `<div title="${escapeAttr(payload)}"></div>`;
        // The escaped payload must not introduce a second '"' that could
        // close the attribute early, nor a bare '<' that could open a new
        // tag inside the attribute value.
        const attrValueMatch = html.match(/title="([^]*?)"/);
        assert.ok(attrValueMatch, `expected a well-formed title attribute for payload ${JSON.stringify(payload)}, got: ${html}`);
    }
});

test('jsStringLiteral escapes both the JS-string and HTML-attribute layers', () => {
    for (const payload of MALICIOUS_PAYLOADS) {
        const encoded = jsStringLiteral(payload);
        // Simulate the browser's real decode order for an inline event
        // handler attribute: the HTML attribute value is HTML-decoded
        // FIRST, and the result is then parsed as JS source.
        const htmlDecoded = encoded
            .replace(/&amp;/g, '&')
            .replace(/&lt;/g, '<')
            .replace(/&gt;/g, '>')
            .replace(/&quot;/g, '"')
            .replace(/&#39;/g, "'");
        // After HTML-decoding, the content must still be safe to splice
        // into a single-quoted JS string literal: no unescaped single
        // quote, backslash, or raw newline may survive.
        assert.doesNotMatch(htmlDecoded, /(^|[^\\])'/, `unescaped single quote survived HTML-decoding for payload ${JSON.stringify(payload)}: ${JSON.stringify(htmlDecoded)}`);
        assert.doesNotMatch(htmlDecoded, /\n/, `raw newline survived for payload ${JSON.stringify(payload)}`);
        assert.doesNotMatch(htmlDecoded, /\r/, `raw carriage return survived for payload ${JSON.stringify(payload)}`);
    }
});

test('jsStringLiteral neutralizes a single-quote breakout inside onclick', () => {
    const payload = `x'); alert(document.cookie); //`;
    const encoded = jsStringLiteral(payload);
    const attrHtml = `onclick="fn('${encoded}')"`;
    // The rendered attribute must not contain an unescaped closing quote
    // that would end the fn(...) argument early once HTML-decoded.
    const htmlDecoded = attrHtml
        .replace(/&amp;/g, '&')
        .replace(/&lt;/g, '<')
        .replace(/&gt;/g, '>')
        .replace(/&quot;/g, '"')
        .replace(/&#39;/g, "'");
    // Between the first `('` and the matching closing `')`, there must be
    // no unescaped raw single quote.
    const inner = htmlDecoded.slice(htmlDecoded.indexOf("('") + 2, htmlDecoded.lastIndexOf("')"));
    assert.doesNotMatch(inner, /(^|[^\\])'/, `single quote breakout survived: ${JSON.stringify(inner)}`);
});

test('jsStringLiteral is null/undefined-safe', () => {
    assert.equal(jsStringLiteral(null), '');
    assert.equal(jsStringLiteral(undefined), '');
});

test('jsonToSafeAttr HTML-escapes serialized JSON so it cannot break out of an attribute', () => {
    for (const payload of MALICIOUS_PAYLOADS) {
        const out = jsonToSafeAttr({ name: payload });
        assert.ok(!out.includes('<'), `expected no raw '<' for payload ${JSON.stringify(payload)}, got: ${out}`);
        assert.ok(!out.includes('>'), `expected no raw '>' for payload ${JSON.stringify(payload)}, got: ${out}`);
        // A JSON string always contains raw '"' characters (key/value
        // quoting) — jsonToSafeAttr's entire job is to make sure NONE of
        // them survive unescaped once embedded in an HTML attribute.
        assert.ok(!out.includes('"'), `expected all '"' to be escaped for payload ${JSON.stringify(payload)}, got: ${out}`);
    }
});

test('jsonToSafeAttr round-trips back to the original value through JSON.parse + HTML-unescape', () => {
    const original = { name: `weird"name'<script>&co`, nested: { note: `line1\nline2` } };
    const attr = jsonToSafeAttr(original);
    const htmlDecoded = attr
        .replace(/&amp;/g, '&')
        .replace(/&lt;/g, '<')
        .replace(/&gt;/g, '>')
        .replace(/&quot;/g, '"')
        .replace(/&#39;/g, "'");
    assert.deepEqual(JSON.parse(htmlDecoded), original);
});

// --- withMaskedSecrets ---

test('withMaskedSecrets masks env values whose key looks like a credential', () => {
    const spec = {
        name: 'my-app',
        env: {
            PASSWORD: 'hunter2',
            API_KEY: 'sk-abc123',
            api_key: 'sk-lower',
            DB_PASSWORD: 'p@ss',
            secret_token: 'tok_xyz',
            PRIVATE_KEY: '-----BEGIN KEY-----',
            SOME_CREDENTIAL: 'cred-val',
            PORT: '8080',
            HOST: '127.0.0.1',
        },
    };
    const masked = withMaskedSecrets(spec);

    for (const key of ['PASSWORD', 'API_KEY', 'api_key', 'DB_PASSWORD', 'secret_token', 'PRIVATE_KEY', 'SOME_CREDENTIAL']) {
        assert.equal(masked.env[key], '••••••••', `expected ${key} to be masked`);
    }
    // Non-sensitive keys must be left untouched.
    assert.equal(masked.env.PORT, '8080');
    assert.equal(masked.env.HOST, '127.0.0.1');
});

test('withMaskedSecrets does not mutate the original object', () => {
    const spec = { name: 'app', env: { TOKEN: 'real-secret-value' } };
    const masked = withMaskedSecrets(spec);
    assert.equal(spec.env.TOKEN, 'real-secret-value', 'original object must be untouched');
    assert.equal(masked.env.TOKEN, '••••••••');
});

test('withMaskedSecrets recurses into proposedSpec (adoption assessment preview)', () => {
    const assessment = {
        containerName: 'legacy-web',
        proposedSpec: {
            name: 'legacy-web',
            env: { SECRET: 'leaked-if-shown' },
        },
    };
    const masked = withMaskedSecrets(assessment);
    assert.equal(masked.proposedSpec.env.SECRET, '••••••••');
    assert.equal(assessment.proposedSpec.env.SECRET, 'leaked-if-shown', 'original must be untouched');
});

test('withMaskedSecrets is a safe no-op for null, non-object, or env-less input', () => {
    assert.equal(withMaskedSecrets(null), null);
    assert.equal(withMaskedSecrets(undefined), undefined);
    assert.equal(withMaskedSecrets('a string'), 'a string');
    const noEnv = { name: 'app' };
    assert.deepEqual(withMaskedSecrets(noEnv), noEnv);
});

// --- Realistic untrusted-source scenarios named in the audit finding ---

test('untrusted container/image names round-trip safely through escapeHtml', () => {
    const names = [
        `"><script>alert(1)</script>`,
        `weird'container"name`,
        `image:tag<script>`,
        `registry.example.com/org/repo@sha256:deadbeef`,
    ];
    for (const name of names) {
        const html = `<span>${escapeHtml(name)}</span>`;
        assert.ok(!html.includes('<script>'), `expected no live <script> tag from name ${JSON.stringify(name)}, got: ${html}`);
    }
});

test('untrusted label/provenance/registry-YAML/process/network values are neutralized', () => {
    const untrustedFields = {
        label: `io.example.note=<img src=x onerror=alert(1)>`,
        provenanceService: `svc"><svg onload=alert(1)>`,
        registryNote: `note: "</script><script>alert(document.domain)</script>"`,
        processName: `evil'); DROP TABLE users; --`,
        networkName: `net<script>alert('net')</script>`,
    };
    for (const [field, value] of Object.entries(untrustedFields)) {
        const escaped = escapeHtml(value);
        assert.ok(!escaped.includes('<script'), `${field}: expected no live <script in escaped output, got: ${escaped}`);
        assert.ok(!escaped.includes('<img'), `${field}: expected no live <img in escaped output, got: ${escaped}`);
        assert.ok(!escaped.includes('<svg'), `${field}: expected no live <svg in escaped output, got: ${escaped}`);
    }
});
