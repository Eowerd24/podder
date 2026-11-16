import test from 'node:test';
import assert from 'node:assert/strict';
import { launcherAction, selectedEndpoint, endpointOptionsHTML, safeWebURL } from './launcher.js';

test('cards require explicit web configuration and a running container', () => {
    for (const kind of ['none', 'gui', 'terminal', undefined]) assert.equal(launcherAction({ running: true, definition: { kind }, endpoints: [{ id: 'a' }] }), '');
    assert.equal(launcherAction({ running: false, definition: { kind: 'web' }, endpoints: [{ id: 'a' }] }), '');
    assert.equal(launcherAction({ running: true, mappings: [{ hostPort: 80 }] }), '');
    assert.equal(launcherAction({ running: true, definition: { kind: 'web' }, endpoints: [{ id: 'a' }] }), 'Open Web');
});
test('multiple endpoints require choice unless explicit preference exists', () => {
    const o = { running: true, definition: { kind: 'web' }, endpoints: [{ id: 'a' }, { id: 'b' }] };
    assert.equal(launcherAction(o), 'Choose Endpoint'); assert.equal(selectedEndpoint(o), null);
    o.definition.preferredEndpointID = 'b'; assert.equal(launcherAction(o), 'Open Web'); assert.equal(selectedEndpoint(o).id, 'b');
});
test('endpoint options escape URLs and never interpolate untrusted IDs', () => {
    const output = endpointOptionsHTML([{ id: '\" onclick="evil()', url: 'http://[::1]:8080/<img onerror="evil()">&\'' }]);
    assert.ok(!output.includes('<img')); assert.ok(!output.includes('onclick')); assert.ok(output.includes('&lt;img')); assert.ok(output.includes('value="0"'));
});
test('copy fallback only accepts credential-free HTTP(S)', () => {
    for (const value of ['http://127.0.0.1:8080/', 'https://[::1]:443/app']) assert.equal(safeWebURL(value), true);
    for (const value of ['javascript:alert(1)', 'file:///etc/passwd', 'http://user:secret@host/', '//evil.test', 'http://host/\n', 'http://host\\evil']) assert.equal(safeWebURL(value), false);
});
