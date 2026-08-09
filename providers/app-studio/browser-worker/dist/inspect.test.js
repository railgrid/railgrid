import assert from 'node:assert/strict';
import test from 'node:test';
import { RequestValidationError, validateInspectRequest } from './inspect.js';
test('validates and normalizes the inspect contract', () => {
    assert.deepEqual(validateInspectRequest({
        url: 'https://preview.example.test/path?capability=secret',
        includeScreenshot: true,
        assertions: [
            { kind: 'text_present', text: 'Ready' },
            { kind: 'role_present', role: 'button', name: 'Save', exact: true },
            { kind: 'role_count', role: 'row', min: 2, max: 5 },
        ],
    }), {
        url: 'https://preview.example.test/path?capability=secret',
        includeScreenshot: true,
        assertions: [
            { kind: 'text_present', text: 'Ready' },
            { kind: 'role_present', role: 'button', name: 'Save', exact: true },
            { kind: 'role_count', role: 'row', min: 2, max: 5 },
        ],
    });
});
test('matches the API limit of twelve assertions', () => {
    const assertion = { kind: 'text_present', text: 'Ready' };
    assert.equal(validateInspectRequest({
        url: 'https://preview.example.test',
        assertions: Array.from({ length: 12 }, () => assertion),
    }).assertions?.length, 12);
    assert.throws(() => validateInspectRequest({
        url: 'https://preview.example.test',
        assertions: Array.from({ length: 13 }, () => assertion),
    }), /at most 12 items/);
});
for (const input of [
    null,
    {},
    { url: 'file:///etc/passwd' },
    { url: 'https://user:secret@example.test' },
    { url: 'https://example.test', extra: true },
    { url: 'https://example.test', assertions: [{ kind: 'role_count', role: 'row', min: 3, max: 1 }] },
    { url: 'https://example.test', assertions: [{ kind: 'unknown' }] },
]) {
    test(`rejects invalid request ${JSON.stringify(input)}`, () => {
        assert.throws(() => validateInspectRequest(input), RequestValidationError);
    });
}
//# sourceMappingURL=inspect.test.js.map