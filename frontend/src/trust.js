// --- Trust boundary: escaping helpers for untrusted data ---
//
// Podman labels, container/image names, Compose project/service metadata,
// registry YAML strings, process names, and network names all originate
// outside Podder's control (a container's own labels, a homelab-wide
// registry file, host process listings, ...) and must never be rendered
// as raw HTML. Prefer textContent/dataset/addEventListener for dynamic
// content; where a template literal must interpolate untrusted text into
// markup, always pass it through escapeHtml first. Do not rely on
// ad hoc `.replace(/"/g, '&quot;')` calls — that only covers straight
// double quotes and leaves '<', '>', '&', and single quotes as breakout
// vectors.
//
// These functions are pure and DOM-free by design, kept in their own
// module (rather than inline in main.js) specifically so they can be unit
// tested with a plain Node test runner instead of a full browser/DOM
// environment — see trust.test.js.
export function escapeHtml(value) {
    if (value === null || value === undefined) return '';
    return String(value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// Alias documenting intent at attribute-value call sites; escapeHtml's
// output (which escapes '"', "'", '&', '<', '>') is already safe inside a
// double- or single-quoted HTML attribute.
export function escapeAttr(value) {
    return escapeHtml(value);
}

// Encodes a value for safe embedding inside a single-quoted JS string
// literal that itself sits inside a double-quoted HTML onclick attribute
// (e.g. onclick="fn('${...}')"). HTML-escaping the outer attribute alone is
// NOT sufficient here: an event-handler attribute's value is HTML-decoded
// by the browser BEFORE being compiled as JS, so an HTML entity for a
// single quote (e.g. &#39;) decodes right back into a literal ' before the
// JS parser ever sees it, and can still terminate the inline string early.
// This backslash-escapes the JS string content first (so a literal quote
// in the value can't break the JS string), then HTML-escapes the result
// (so it can't break the HTML attribute either) — either step alone is
// insufficient for this specific nested context.
export function jsStringLiteral(value) {
    const jsEscaped = String(value === null || value === undefined ? '' : value)
        .replace(/\\/g, '\\\\')
        .replace(/'/g, "\\'")
        .replace(/\n/g, '\\n')
        .replace(/\r/g, '\\r')
        .replace(/\u2028/g, '\\u2028')
        .replace(/\u2029/g, '\\u2029');
    return escapeHtml(jsEscaped);
}

// Encodes a JSON-serializable value for safe embedding as an HTML
// attribute value (e.g. inside onclick="fn(...)"), escaping every
// character that could break out of the attribute or reopen markup —
// unlike a bare `.replace(/"/g, '&quot;')`, which leaves '<', '>', '&',
// and single quotes unescaped.
export function jsonToSafeAttr(value) {
    return escapeHtml(JSON.stringify(value));
}

// Case-insensitive fragments of environment variable / spec field names
// treated as sensitive. Matching values are masked by default wherever a
// spec or adoption preview is rendered, since a stored spec's Env map can
// contain real credentials. The underlying value is never altered on disk
// — only its on-screen rendering is masked.
export const SENSITIVE_KEY_PATTERNS = /password|token|secret|api[_-]?key|private[_-]?key|credential/i;

export function maskSensitiveValue() {
    return '••••••••';
}

// Returns a deep-cloned spec/assessment object with values masked for any
// key matching SENSITIVE_KEY_PATTERNS (currently just the `env` map, the
// one place a spec carries values that can plausibly be credentials).
export function withMaskedSecrets(spec) {
    if (!spec || typeof spec !== 'object') return spec;
    const clone = JSON.parse(JSON.stringify(spec));
    if (clone.env && typeof clone.env === 'object') {
        for (const key of Object.keys(clone.env)) {
            if (SENSITIVE_KEY_PATTERNS.test(key)) {
                clone.env[key] = maskSensitiveValue();
            }
        }
    }
    if (clone.proposedSpec) {
        clone.proposedSpec = withMaskedSecrets(clone.proposedSpec);
    }
    return clone;
}
