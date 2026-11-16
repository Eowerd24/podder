import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { bindActions } from './actions.js';
const listeners = {}, calls = [];
bindActions({ addEventListener: (type, fn) => { listeners[type] = fn; } }, { edit: (...args) => calls.push(args) });
function dispatch(type, attrs, fields = {}) {
    const element = { getAttribute: key => attrs[key] ?? null, ...fields };
    listeners[type]({ target: { closest: () => element } });
}
const payload = `'); globalThis.compromised = true; // <img onerror=alert(1)>`;
dispatch('click', { 'data-click': 'edit', 'data-click-args': JSON.stringify([payload]) });
assert.deepEqual(calls, [[payload]]);
assert.equal(globalThis.compromised, undefined);
dispatch('input', { 'data-input': 'edit', 'data-input-args': '[4,"hostPort"]', 'data-input-field': 'value' }, { value: '8080' });
assert.deepEqual(calls[1], [4, 'hostPort', '8080']);
dispatch('change', { 'data-change': 'edit', 'data-change-field': 'checked' }, { checked: true });
assert.deepEqual(calls[2], [true]);
for (const attrs of [
    { 'data-click': 'constructor', 'data-click-args': '[]' },
    { 'data-click': 'edit', 'data-click-args': 'alert(1)' },
    { 'data-click': 'edit', 'data-click-args': '{}' },
]) dispatch('click', attrs);
dispatch('click', { 'data-click': 'edit' }, { disabled: true });
assert.equal(calls.length, 3);
const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8');
const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
assert.match(html, /script-src 'self';/);
assert.doesNotMatch(html + main, /\son(?:click|change|input)\s*=/);
for (const [, name] of (html + main).matchAll(/data-(?:click|change|input)="(\w+)"/g)) {
    assert.ok(main.includes(`${name}: (...args) => window.${name}(...args)`) || main.includes(`${name}: (...args) => ${name}(...args)`), name);
    assert.ok(main.includes(`window.${name} =`) || main.includes(`function ${name}(`), name);
}
