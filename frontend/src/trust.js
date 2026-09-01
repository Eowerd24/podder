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

// --- Bind-address terminology & port-range display helpers ---
//
// Podder must never claim Internet-public exposure merely because a mapping
// binds a wildcard address -- reachability from the wider Internet depends
// on routing/NAT/firewall rules Podder cannot see. It also must not display
// an omitted host address as though it literally said "0.0.0.0": those are
// distinct declarations (see DeclaredEndpointsEquivalent in endpoint.go).
// Kept here (DOM-free, pure) alongside the escaping helpers so both can be
// unit tested with a plain Node test runner -- see trust.test.js.

// describeBindAddress classifies a raw bind address string for display,
// distinguishing an omitted/default bind from an explicit IPv4/IPv6
// wildcard and from IPv4/IPv6 loopback: DEFAULT / IPv4 ANY / IPv6 ANY /
// LOOPBACK / IPv6 LOOPBACK / SPECIFIC.
export function describeBindAddress(raw) {
    const bind = (raw === undefined || raw === null) ? '' : String(raw).trim();
    if (bind === '') {
        return {
            category: 'default',
            display: 'DEFAULT',
            detail: 'Host address not explicitly set; Podman applies its own default bind for this mapping.'
        };
    }
    if (bind === '0.0.0.0' || bind === '*') {
        return {
            category: 'wildcard4',
            display: bind === '*' ? '0.0.0.0' : bind,
            detail: 'IPv4 ANY (0.0.0.0): all local IPv4 interfaces -- network reachable subject to routing and firewall rules, not necessarily Internet-public.'
        };
    }
    if (bind === '::') {
        return {
            category: 'wildcard6',
            display: '::',
            detail: 'IPv6 ANY (::): all local IPv6 interfaces -- network reachable subject to routing and firewall rules, not necessarily Internet-public.'
        };
    }
    if (bind === '127.0.0.1') {
        return { category: 'loopback4', display: bind, detail: 'LOOPBACK: this host only.' };
    }
    if (bind === '::1') {
        return { category: 'loopback6', display: bind, detail: 'IPv6 LOOPBACK: this host only.' };
    }
    if (bind.toLowerCase() === 'localhost') {
        return { category: 'loopback4', display: bind, detail: 'LOOPBACK: this host only.' };
    }
    return {
        category: 'specific',
        display: bind,
        detail: 'SPECIFIC: a single named interface -- network reachable subject to routing and firewall rules.'
    };
}

// formatPortRangeSuffix renders a single port, or (when rangeSize > 1) an
// inclusive "start-end" range string, for DISPLAY only -- never for a
// Compose/Quadlet configuration snippet, which must always go through the
// backend's canonical FormatPublishSpec (see PreviewComposeSnippet /
// PreviewQuadletSnippet in main.js). A range must always show its full
// span; showing only the first port would silently hide the rest of the
// range from the operator.
export function formatPortRangeSuffix(start, rangeSize) {
    const s = parseInt(start, 10);
    const n = parseInt(rangeSize, 10);
    if (isNaN(s)) return String(start != null ? start : '');
    if (n && n > 1) {
        return `${s}-${s + n - 1}`;
    }
    return String(s);
}

// --- Registry reconciliation lifecycle status presentation (v1.4 round 2) ---
//
// Explicit visual + tooltip semantics for EVERY registry reconciliation
// lifecycle status the backend can report (see classifyRegistryMatch /
// classifyRegistryMissing / registryStateExpectsBindMatch in registry.go,
// and the OWNER_MISMATCH/OWNER_UNKNOWN outcomes in ports.go). Falling back
// to a generic/blank presentation for a status this map doesn't recognize
// would make correct backend semantics invisible to the operator -- keep
// this in sync with every value those Go functions can produce. Kept here
// (DOM-free, pure data) so completeness can be unit tested -- see
// trust.test.js.
//
// `cls` names a `.reconcile-pill.<cls>` CSS class (see style.css); the
// palette groups by meaning per the v1.4 hardening review:
//   match      = green  (good / fulfilled as declared)
//   undeclared = gray   (informational / expected absence)
//   reserved   = cyan   (informational / currently running but not
//                        ordinary permanent state)
//   planned    = purple (informational future intent)
//   missing    = amber  (attention: still permitted but flagged, or an
//                        active declaration that isn't running)
//   conflict   = red    (drift/problem: needs operator attention)
//   host       = indigo (no registry involvement at all)
export const RECONCILIATION_STATUS_META = {
    MATCH: { label: 'MATCH', cls: 'match', tooltip: 'Matches the declared registry entry.' },
    UNDECLARED: { label: 'UNDECLARED', cls: 'undeclared', tooltip: 'Running workload is not registered in the external registry.' },
    DECLARED_MISSING: { label: 'MISSING', cls: 'missing', tooltip: 'Declared as active in the registry, but is not currently running.' },
    DECLARED_ENDPOINT_MISMATCH: { label: 'BIND MISMATCH', cls: 'conflict', tooltip: 'The declared and observed bind addresses differ.' },
    RESERVED_FREE: { label: 'RESERVED', cls: 'reserved', tooltip: 'Reserved in the registry and currently unused, as expected.' },
    RESERVED_IN_USE: { label: 'RESERVED (IN USE)', cls: 'conflict', tooltip: 'Reserved in the registry, but something is occupying it.' },
    PLANNED: { label: 'PLANNED', cls: 'planned', tooltip: 'Declared as a future/planned service; not expected to be running yet.' },
    UNSCOPED: { label: 'UNSCOPED', cls: 'planned', tooltip: 'Registry record has no node scope; informational only.' },
    REMOTE: { label: 'REMOTE', cls: 'planned', tooltip: 'Registry record belongs to a different node.' },
    TEMPORARY_ACTIVE: { label: 'TEMPORARY', cls: 'reserved', tooltip: 'Declared as temporary and currently running -- not ordinary permanent active state.' },
    TEMPORARY_MISSING: { label: 'TEMPORARY (IDLE)', cls: 'undeclared', tooltip: 'Declared as temporary and not currently running -- expected, not a fault.' },
    DEPRECATED_ACTIVE: { label: 'DEPRECATED', cls: 'missing', tooltip: 'Still running, but marked deprecated in the registry and flagged for migration/removal.' },
    DEPRECATED_MISSING: { label: 'DEPRECATED (IDLE)', cls: 'undeclared', tooltip: 'Marked deprecated in the registry and not currently running.' },
    RETIRED_IN_USE: { label: 'RETIRED (IN USE)', cls: 'conflict', tooltip: 'Registry marks this endpoint retired, but it is still active -- useful drift information.' },
    RETIRED_FREE: { label: 'RETIRED', cls: 'undeclared', tooltip: 'Registry marks this endpoint retired; it is correctly not running.' },
    OWNER_MISMATCH: { label: 'OWNER MISMATCH', cls: 'conflict', tooltip: 'Endpoint matches the declaration, but the observed workload owner differs.' },
    OWNER_UNKNOWN: { label: 'OWNER UNKNOWN', cls: 'conflict', tooltip: 'Endpoint matches the declaration, but the observed workload owner could not be confidently identified.' },
};

// reconcile-pill classes don't have a 1:1 twin in the status-pill palette
// (status-pill has no 'match'/'undeclared'/'host' variant) -- this maps a
// RECONCILIATION_STATUS_META class onto the closest status-pill class for
// standalone registry-declared rows, which reuse the SAME status strings
// as reconciliationStatus (see GetPortOverview's unmatched-declared loop in
// ports.go).
export const STATUS_PILL_CLASS_FOR_RECONCILE_CLASS = {
    match: 'active', undeclared: 'stopped', host: 'stopped',
    missing: 'missing', reserved: 'reserved', planned: 'planned', conflict: 'conflict'
};

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
