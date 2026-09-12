import { afterEach, expect, it, vi } from 'vitest';
import { API, OperationError, type FarosContext } from './api';
afterEach(() => vi.useRealTimers());
function client(fetch: FarosContext['fetch'], signal = new AbortController().signal) { return new API({ tenant: 'workspace', fetch }, 'default', signal); }
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
