import { afterEach, expect, it, vi } from 'vitest';
import { API, OperationError, type FarosContext } from './api';
afterEach(() => vi.useRealTimers());
it('deletes only the exact Connection UID as the caller and accepts an empty response', async () => {
  const calls: { path: string; init?: RequestInit }[] = [];
  const api = client(async (path, init) => { calls.push({ path: String(path), init }); return new Response(null, { status: 204 }); });
  await api.deleteConnection('main', 'original-uid');
  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe('/clusters/workspace/apis/linear.providers.faros.sh/v1alpha1/connections/main');
  expect(calls[0].init?.method).toBe('DELETE');
  expect(JSON.parse(String(calls[0].init?.body))).toMatchObject({ preconditions: { uid: 'original-uid' } });
  await expect(api.deleteConnection('main', '')).rejects.toThrow('Refresh');
  expect(calls).toHaveLength(1);
});
it.each([403, 409, 404])('preserves deletion errors except already-absent resources (%s)', async status => {
  const api = client(async () => new Response('', { status }));
  if (status === 404) await expect(api.deleteConnection('main', 'uid')).resolves.toBeUndefined();
  else await expect(api.deleteConnection('main', 'uid')).rejects.toMatchObject({ status });
});
function client(fetch: FarosContext['fetch'], signal = new AbortController().signal) { return new API({ tenant: 'workspace', fetch }, signal); }
it('follows resource continuation and discovery cursors without truncating inventories', async () => {
  const paths: string[] = [];
  const api = client(async input => { const path = String(input); paths.push(path); return Response.json(path.includes('continue=') ? { items: [{ metadata: { name: 'second' } }] } : { items: [{ metadata: { name: 'first' } }], metadata: { continue: 'next/a+b' } }); });
  expect((await api.list('connections')).items.map(r => r.metadata.name)).toEqual(['first', 'second']);
  expect(paths[1]).toContain('continue=next%2Fa%2Bb');
  const pages: Record<string, unknown>[] = [];
  const discovery = client(async (path, init) => {
    if (String(path).includes('/teams?')) return Response.json({ items: [registeredTeam] });
    const input = JSON.parse(String(init?.body)).input; pages.push(input);
    return Response.json({ result: { phase: 'Succeeded', result: { nodes: [{ id: pages.length === 1 ? 'one' : 'two' }], pageInfo: { hasNextPage: pages.length === 1, endCursor: 'cursor' } } } });
  });
  expect((await discovery.discover('linear', 'states', 'team')).map(n => n.id)).toEqual(['one', 'two']);
  expect(pages[1]).toEqual({ after: 'cursor', first: 50 });
});
const registeredTeam = { metadata: { name: 'engineering', uid: 'team-uid' }, spec: { connection: 'linear', teamID: 'team' } };
it('retains a lost write response and inspects without submitting a second write', async () => {
  const writes: Record<string, import('./api').WriteIntent> = {};
  const calls: { path: string; method?: string; body?: any }[] = [];
  const api = new API({ tenant: 'workspace', fetch: async (path, init) => {
    const request = { path: String(path), method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined }; calls.push(request);
    if (request.path.includes('/teams?')) return Response.json({ items: [registeredTeam] });
    if (init?.method === 'POST') throw new Error('lost response');
    return Response.json({ result: { phase: 'Succeeded', result: { id: 'created' } } });
  } }, new AbortController().signal, writes);
  await expect(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Draft' })).rejects.toBeInstanceOf(OperationError);
  const name = writes.createIssue.name;
  expect(name).toMatch(/^\d{8}T\d{6}Z\./);
  expect(calls[1].path).toBe('/services/providers/linear/actions/clusters/workspace/teams/engineering/create_issue/v1');
  expect(calls[1].body).toEqual({ requestId: name, input: { title: 'Draft' } });
  await expect(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Again' })).rejects.toMatchObject({ operationName: name });
  expect(await api.inspectWrite(name)).toEqual({ id: 'created' });
  expect(calls.filter(c => c.method === 'POST')).toHaveLength(1);
  expect(calls.at(-1)?.path).toContain('?requestId=' + encodeURIComponent(name));
  expect(writes.createIssue.name).toBe(name);
  api.acknowledgeWrites(); expect(writes).toEqual({});
});
it.each(['Running', 'Uncertain'])('does not claim a %s write succeeded or automatically replay it', async phase => {
  let posts = 0;
  const api = client(async (path, init) => {
    if (String(path).includes('/teams?')) return Response.json({ items: [registeredTeam] });
    if (init?.method === 'POST') posts++;
    return Response.json({ result: { phase } });
  });
  await expect(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Draft' })).rejects.toBeInstanceOf(OperationError);
  expect(posts).toBe(1);
});
it('retains the resource binding when a write request is aborted', async () => {
  const controller = new AbortController();
  const writes: Record<string, import('./api').WriteIntent> = {};
  let posts = 0;
  const fetch: FarosContext['fetch'] = async (path, init) => {
    if (String(path).includes('/teams?')) return Response.json({ items: [registeredTeam] });
    if (init?.method === 'POST') { posts++; controller.abort(); }
    return Response.json({ result: { phase: 'Succeeded', result: { id: 'created' } } });
  };
  const api = new API({ tenant: 'workspace', fetch }, controller.signal, writes);
  await expect(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Draft' })).rejects.toMatchObject({ name: 'AbortError' });
  expect(writes.createIssue).toMatchObject({ connection: 'linear', team: 'engineering', action: 'create_issue' });
  const returned = new API({ tenant: 'workspace', fetch }, new AbortController().signal, writes);
  await expect(returned.action('other', { action: 'createIssue', title: 'Draft' })).rejects.toMatchObject({ operationName: writes.createIssue.name });
  expect(await returned.inspectWrite(writes.createIssue.name)).toEqual({ id: 'created' });
  expect(posts).toBe(1);
});

it('recovers a lost connection response only when the exact named intent matches', async () => {
  let posts = 0; const paths: string[] = [];
  const api = client(async (input, init) => {
    paths.push(String(input)); if (init?.method === 'POST') { posts++; throw new Error('lost response'); }
    return Response.json({ metadata: { name: 'linear' }, spec: { apiKeySecretRef: { name: 'key', key: 'apiKey' } } });
  });
  expect((await api.createConnection('linear', 'key')).metadata.name).toBe('linear');
  expect(posts).toBe(1); expect(paths[1]).toMatch(/connections\/linear$/);
  await expect(api.createConnection('linear', 'different')).rejects.toThrow('different settings');
});
it('bounds resource reads to one page and preserves opaque cursors', async () => {
  const paths: string[] = []; const api = client(async input => { paths.push(String(input)); return Response.json({ items: [], metadata: { continue: 'next' } }); });
  await api.listPage('teams', 10, 'a/b+c'); expect(paths).toHaveLength(1); expect(paths[0]).toContain('limit=10&continue=a%2Fb%2Bc');
});
it('writes workspace resources while keeping credential namespace in the Secret reference', async () => {
  const calls: { path: string; body: any }[] = [];
  const api = client(async (input, init) => { const body = init?.body ? JSON.parse(String(init.body)) : {}; calls.push({ path: String(input), body }); return Response.json(body); });
  await api.createConnection('linear', 'key', 'credentials');
  expect(calls[0].path).toBe('/clusters/workspace/apis/linear.providers.faros.sh/v1alpha1/connections');
  expect(calls[0].body.metadata).toEqual({ name: 'linear' });
  expect(calls[0].body.spec.apiKeySecretRef).toEqual({ name: 'key', namespace: 'credentials', key: 'apiKey' });
});

it('saves a caller-scoped credential owned by the connection, never in its spec', async () => {
  const calls: { path: string; body: any; method: string }[] = [];
  const api = client(async (path, init) => {
    const body = JSON.parse(String(init?.body)); calls.push({ path: String(path), body, method: init!.method! });
    return Response.json({ ...body, metadata: { ...body.metadata, uid: 'connection-uid' } });
  });
  await api.connectWithKey('engineering', 'private-key', 'unique-key');
  expect(calls).toHaveLength(2);
  expect(JSON.stringify(calls[0])).not.toContain('private-key');
  expect(calls[0].body.spec).not.toHaveProperty('teams');
  expect(calls[0].body.spec).not.toHaveProperty('teamPolicy');
  expect(calls[1].path).toBe('/clusters/workspace/api/v1/namespaces/default/secrets');
  expect(calls[1].body.stringData).toEqual({ apiKey: 'private-key' });
  expect(calls[1].body.metadata.ownerReferences).toEqual([{ apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', name: 'engineering', uid: 'connection-uid' }]);
});

it.each([true, false])('recovers a lost credential response only for the exact owned secret (owned=%s)', async owned => {
  const methods: string[] = [];
  const api = client(async (_path, init) => {
    methods.push(init!.method!);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    if (body?.kind === 'Connection') return Response.json({ ...body, metadata: { name: 'engineering', uid: 'connection-uid' } });
    if (body?.kind === 'Secret') throw new Error('lost response');
    return Response.json({ metadata: { ownerReferences: [{ apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', uid: owned ? 'connection-uid' : 'foreign-uid' }] }, data: { apiKey: btoa('private-key') } });
  });
  const result = api.connectWithKey('engineering', 'private-key', 'unique-key');
  if (owned) expect((await result).metadata.name).toBe('engineering');
  else await expect(result).rejects.toMatchObject({ connectionName: 'engineering' });
  expect(methods).toEqual(['POST', 'POST', 'GET']);
});
