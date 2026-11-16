import test from 'node:test';
import assert from 'node:assert/strict';
import { parseSessionEnv, parseSessionCommand } from './session-ui.js';
test('session environment preserves values and rejects ambiguity', () => {
    assert.deepEqual(parseSessionEnv('A=value with spaces\nTOKEN=one=two\n'), { A: 'value with spaces', TOKEN: 'one=two' });
    assert.throws(() => parseSessionEnv('TOKEN')); assert.throws(() => parseSessionEnv('A=one\nA=two'));
    const value = parseSessionEnv('__proto__=safe'); assert.equal(Object.getPrototypeOf(value), Object.prototype); assert.equal(value.__proto__, 'safe');
});
test('command arguments remain structured and never get shell-split', () => {
    assert.deepEqual(parseSessionCommand('["printf", "space ; $(touch nope)", "x=y"]'), ['printf', 'space ; $(touch nope)', 'x=y']);
    assert.throws(() => parseSessionCommand('echo hi')); assert.throws(() => parseSessionCommand('["echo", 2]'));
});
