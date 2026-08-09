import assert from 'node:assert/strict';
import test from 'node:test';
import { createServer } from './server.js';
const response = {
    status: 'succeeded',
    finalURL: 'https://preview.example.test/',
    title: 'Preview',
    snapshot: '- heading "Preview"',
    assertions: [],
    console: [],
    network: [],
};
test('serves health and delegates inspection', async (t) => {
    let received;
    let healthChecks = 0;
    const inspector = {
        health: async () => { healthChecks++; },
        inspect: async (input) => {
            received = input;
            return response;
        },
        close: async () => undefined,
    };
    const server = createServer(inspector);
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    t.after(() => server.close());
    const port = server.address().port;
    const health = await fetch(`http://127.0.0.1:${port}/healthz`);
    assert.equal(health.status, 200);
    assert.deepEqual(await health.json(), { status: 'ok' });
    assert.equal(healthChecks, 1);
    const inspected = await fetch(`http://127.0.0.1:${port}/v1/inspect`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ url: 'https://preview.example.test/' }),
    });
    assert.equal(inspected.status, 200);
    assert.deepEqual(await inspected.json(), response);
    assert.deepEqual(received, { url: 'https://preview.example.test/' });
});
test('rejects malformed JSON and unknown routes', async (t) => {
    const server = createServer({ health: async () => undefined, inspect: async () => response, close: async () => undefined });
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    t.after(() => server.close());
    const port = server.address().port;
    const malformed = await fetch(`http://127.0.0.1:${port}/v1/inspect`, { method: 'POST', body: '{' });
    assert.equal(malformed.status, 400);
    const missing = await fetch(`http://127.0.0.1:${port}/missing`);
    assert.equal(missing.status, 404);
});
test('reports unavailable when Chromium health verification fails', async (t) => {
    const server = createServer({
        health: async () => { throw new Error('launch failed'); },
        inspect: async () => response,
        close: async () => undefined,
    });
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    t.after(() => server.close());
    const port = server.address().port;
    const health = await fetch(`http://127.0.0.1:${port}/healthz`);
    assert.equal(health.status, 503);
    assert.deepEqual(await health.json(), { status: 'unavailable' });
});
test('bounds concurrent inspections', async (t) => {
    let release;
    const blocked = new Promise((resolve) => { release = resolve; });
    let started = 0;
    let bothStarted;
    const ready = new Promise((resolve) => { bothStarted = resolve; });
    const server = createServer({
        health: async () => undefined,
        inspect: async () => {
            started++;
            if (started === 2)
                bothStarted();
            await blocked;
            return response;
        },
        close: async () => undefined,
    });
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    t.after(() => server.close());
    const port = server.address().port;
    const inspect = () => fetch(`http://127.0.0.1:${port}/v1/inspect`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ url: 'https://preview.example.test/' }),
    });
    const first = inspect();
    const second = inspect();
    await ready;
    const rejected = await inspect();
    assert.equal(rejected.status, 429);
    release();
    assert.equal((await first).status, 200);
    assert.equal((await second).status, 200);
});
//# sourceMappingURL=server.test.js.map