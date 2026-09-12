import { afterEach, expect, it, vi } from 'vitest';
import { API, OperationError, type FarosContext } from './api';
afterEach(() => vi.useRealTimers());
function client(fetch: FarosContext['fetch'], signal = new AbortController().signal) { return new API({ tenant: 'workspace', fetch }, signal); }
it('follows resource continuation and discovery cursors without truncating inventories', async () => {
  const paths: string[] = [];
  const api = client(async input => { const path = String(input); paths.push(path); return Response.json(path.includes('continue=') ? { items: [{ metadata: { name: 'second' } }] } : { items: [{ metadata: { name: 'first' } }], metadata: { continue: 'next/a+b' } }); });
  expect((await api.list('connections')).items.map(r => r.metadata.name)).toEqual(['first', 'second']);
  expect(paths[1]).toContain('continue=next%2Fa%2Bb');
  const pages: Record<string, unknown>[] = [];
  const discovery = client(async (_input, init) => {
    if (init?.body) { const op = JSON.parse(String(init.body)); pages.push(op.spec); return Response.json(op); }
    return Response.json({ status: { phase: 'Succeeded', result: { nodes: [{ id: pages.length === 1 ? 'one' : 'two' }], pageInfo: { hasNextPage: pages.length === 1, endCursor: 'cursor' } } } });
  });
  expect((await discovery.discover('linear', 'states', 'team')).map(n => n.id)).toEqual(['one', 'two']);
  expect(pages[1]).toMatchObject({ teamID: 'team', after: 'cursor', first: 50 });
});
it('retains operation identity when polling fails and never resubmits', async () => {
  let posts = 0; let name = '';
  const api = client(async (_input, init) => {
    if (init?.body) { posts++; name = JSON.parse(String(init.body)).metadata.name; return Response.json({}); }
    throw new Error('offline');
  });
  const error = await api.operation('linear', { action: 'updateIssue', issueID: 'issue' }).catch(e => e);
  expect(error).toBeInstanceOf(OperationError); expect(error.operationName).toBe(name); expect(error.message).toContain('status could not be read'); expect(posts).toBe(1);
});
it('reports a still-pending operation after bounded polling without claiming success', async () => {
  vi.useFakeTimers(); let posts = 0; let reads = 0;
  const api = client(async (_input, init) => { if (init?.body) { posts++; return Response.json({}); } reads++; return Response.json({ status: { phase: 'Running' } }); });
  const result = api.operation('linear', { action: 'createIssue', title: 'Example' }).catch(e => e);
  await vi.runAllTimersAsync();
  const error = await result; expect(error).toBeInstanceOf(OperationError); expect(error.message).toContain('still pending'); expect(posts).toBe(1); expect(reads).toBe(30);
});
it('stops polling on abort without replaying a write', async () => {
  vi.useFakeTimers(); const controller = new AbortController(); let posts = 0;
  const api = client(async (_input, init) => { if (init?.body) posts++; return Response.json({ status: { phase: 'Running' } }); }, controller.signal);
  const result = api.operation('linear', { action: 'addComment', body: 'Example' }).catch(e => e);
  await vi.advanceTimersByTimeAsync(0); controller.abort(); await vi.runAllTimersAsync();
  expect((await result).name).toBe('AbortError'); expect(posts).toBe(1);
});

it('recovers a lost connection response only when the exact named intent matches', async () => {
  let posts = 0; const paths: string[] = [];
  const api = client(async (input, init) => {
    paths.push(String(input)); if (init?.method === 'POST') { posts++; throw new Error('lost response'); }
    return Response.json({ metadata: { name: 'linear' }, spec: { apiKeySecretRef: { name: 'key', key: 'apiKey' }, teams: [{ id: 'one' }] } });
  });
  expect((await api.createConnection('linear', 'key', ['one'])).metadata.name).toBe('linear');
  expect(posts).toBe(1); expect(paths[1]).toMatch(/connections\/linear$/);
  await expect(api.createConnection('linear', 'different', ['one'])).rejects.toThrow('different settings');
});
it('bounds history reads to one page and preserves opaque cursors', async () => {
  const paths: string[] = []; const api = client(async input => { paths.push(String(input)); return Response.json({ items: [], metadata: { continue: 'next' } }); });
  await api.listPage('operations', 10, 'a/b+c'); expect(paths).toHaveLength(1); expect(paths[0]).toContain('limit=10&continue=a%2Fb%2Bc');
});
it('patches only team policy with a resource version concurrency fence', async () => {
  let sent: RequestInit | undefined; const api = client(async (_input, init) => { sent = init; return Response.json({}); });
  await api.updateTeams('linear', ['one'], '12'); expect(sent?.method).toBe('PATCH');
  expect(JSON.parse(String(sent?.body))).toEqual({ metadata: { resourceVersion: '12' }, spec: { teams: [{ id: 'one' }] } });
});
it('writes workspace resources while keeping credential namespace in the Secret reference', async () => {
  const calls: { path: string; body: any }[] = [];
  const api = client(async (input, init) => { const body = init?.body ? JSON.parse(String(init.body)) : {}; calls.push({ path: String(input), body }); return Response.json(body); });
  await api.createConnection('linear', 'key', ['one'], 'credentials');
  expect(calls[0].path).toBe('/clusters/workspace/apis/linear.providers.faros.sh/v1alpha1/connections');
  expect(calls[0].body.metadata).toEqual({ name: 'linear' });
  expect(calls[0].body.spec.apiKeySecretRef).toEqual({ name: 'key', namespace: 'credentials', key: 'apiKey' });
});
