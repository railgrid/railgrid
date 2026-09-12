import { afterEach, describe, expect, it } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { authorityKey, parseRoute, issuePath } from './routes';
import { updateFields } from './state';
import type { FarosContext } from './api';

const wrappers: VueWrapper[] = [];
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); document.body.innerHTML = ''; });
const connection = { metadata: { name: 'linear', resourceVersion: '1' }, spec: { apiKeySecretRef: { name: 'linear-key' }, teams: [{ id: 'team' }] }, status: { ready: true } };
const issue = { id: 'issue', identifier: 'ENG-1', title: 'Ship resource pages', description: '<script>not executable</script>', team: { id: 'team', name: 'Engineering' }, state: { id: 'todo', name: 'Todo' } };
function fixture() {
  const calls: { path: string; body?: any }[] = [];
  const operations = new Map<string, any>();
  let fail = false;
  let uncertain = false;
  const ctx: FarosContext = { tenant: 'workspace', user: { sub: 'user' }, subPath: '', fetch: async (input, init) => {
    const path = String(input); const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ path, body });
    if (fail) return new Response('', { status: 503 });
    if (body?.kind === 'Connection') return Response.json(body);
    if (body?.kind === 'Operation') { operations.set(body.metadata.name, body); return Response.json(body); }
    const name = path.split('/').pop()!;
    if (operations.has(name)) {
      const op = operations.get(name); const s = op.spec;
      const result = s.action === 'teams' ? { nodes: [{ id: 'team', name: 'Engineering', key: 'ENG' }] }
        : s.action === 'states' ? { nodes: [{ id: 'done', name: 'Done' }] }
        : s.action === 'issues' ? { nodes: [s.after ? { ...issue, id: 'second', identifier: 'ENG-2' } : issue], pageInfo: { hasNextPage: !s.after, endCursor: 'next' } }
        : s.action === 'comments' ? { nodes: [{ id: 'comment', body: 'Reviewed', user: { displayName: 'Reviewer' }, createdAt: '2026-09-12T12:00:00Z', editedAt: '2026-09-12T13:00:00Z', parentId: 'parent', url: 'https://linear.app/team/issue/ENG-1#comment' }] }
        : { ...issue, ...(s.title ? { title: s.title } : {}), ...(s.description !== undefined ? { description: s.description } : {}) };
      return Response.json({ ...op, status: { phase: uncertain ? 'Uncertain' : 'Succeeded', result, message: uncertain ? 'Check Linear before repeating' : '' } });
    }
    if (path.includes('/connections/')) return Response.json(connection);
    if (path.includes('/connections?')) return Response.json({ items: [connection] });
    return Response.json({ items: [] });
  } };
  return { ctx, calls, setFail: () => { fail = true; }, setUncertain: () => { uncertain = true; } };
}
async function render(ctx: FarosContext) {
  const wrapper = mount(App, { props: { ctx }, attachTo: document.body }); wrappers.push(wrapper);
  wrapper.element.addEventListener('faros-navigate', (event: Event) => { const path = (event as CustomEvent).detail.path; void wrapper.setProps({ ctx: { ...wrapper.props('ctx'), subPath: path } }); });
  await flushPromises(); return wrapper;
}
async function click(w: VueWrapper, text: string) {
  const button = w.findAll('button').find(b => b.text() === text);
  expect(button, `button ${text}`).toBeTruthy(); await button!.trigger('click'); await flushPromises();
}
async function choose(w: VueWrapper, id: string, label: string) {
  await w.get(id).trigger('click'); await flushPromises();
  const option = [...document.querySelectorAll('[role="option"]')].find(el => el.textContent?.includes(label));
  expect(option, label).toBeTruthy(); (option as HTMLElement).click(); await flushPromises();
}

describe('Linear resource journeys', () => {
  it('routes encoded resource identities and keeps authority stable through token rotation', () => {
    expect(parseRoute('')).toEqual({ page: 'connections' });
    expect(parseRoute('namespaces/another/operations/op-1')).toEqual({ page: 'operations', name: 'op-1', namespace: 'another' });
    expect(parseRoute('create/connection').create).toBe('connection');
    expect(parseRoute(issuePath('conn/name', 'issue/id'))).toEqual({ page: 'issues', connection: 'conn/name', name: 'issue/id' });
    expect(parseRoute('issues/%oops/x').invalid).toBe(true);
    expect(authorityKey({ tenant: 'a', token: 'old' })).toBe(authorityKey({ tenant: 'a', token: 'new' }));
    expect(updateFields('', '', 'done')).toEqual({ stateID: 'done' });
  });
  it('creates a Secret-reference connection and opens its detail with working back navigation', async () => {
    const f = fixture(); const w = await render(f.ctx);
    expect(w.text()).toContain('Connections'); expect(w.text()).toContain('linear');
    await click(w, 'Add connection'); expect(w.find('nav').exists()).toBe(false);
    await w.get('#connection-name').setValue('new-connection'); await w.get('#connection-secret').setValue('existing-secret'); await w.get('#connection-teams').setValue('team, other');
    await w.get('form').trigger('submit'); await flushPromises();
    const sent = f.calls.find(c => c.body?.kind === 'Connection')!.body;
    expect(sent.spec).toEqual({ apiKeySecretRef: { name: 'existing-secret', key: 'apiKey' }, teams: [{ id: 'team' }, { id: 'other' }] });
    expect(w.text()).toContain('new-connection'); expect(w.text()).toContain('Secret reference');
    await w.get('a').trigger('click'); await flushPromises(); expect(w.find('nav').exists()).toBe(true);
  });
  it('discovers teams, searches and pages issues, preserves the collection, updates and comments', async () => {
    const f = fixture(); const w = await render({ ...f.ctx, subPath: 'issues' });
    await choose(w, '#linear-connection', 'linear'); await choose(w, '#linear-team', 'Engineering');
    await w.get('#linear-query').setValue('resource'); await w.get('form.linear-fields').trigger('submit'); await flushPromises();
    expect(w.text()).toContain('ENG-1'); await click(w, 'Next'); expect(w.text()).toContain('ENG-2');
    const search = f.calls.filter(c => c.body?.spec.action === 'issues'); expect(search.at(-1)!.body.spec.after).toBe('next');
    await click(w, 'ENG-2'); expect(w.text()).toContain('Update issue'); expect(w.find('script').exists()).toBe(false);
    await w.get('#issue-title').setValue('Updated title'); await w.findAll('form')[0].trigger('submit'); await flushPromises();
    const update = f.calls.find(c => c.body?.spec.action === 'updateIssue')!.body.spec;
    expect(update.title).toBe('Updated title'); expect(update).not.toHaveProperty('description'); expect(update).not.toHaveProperty('stateID');
    expect(w.text()).toContain('Issue updated.');
    await w.get('#issue-comment').setValue('A comment'); await w.findAll('form')[1].trigger('submit'); await flushPromises();
    expect(f.calls.find(c => c.body?.spec.action === 'addComment')!.body.spec.body).toBe('A comment'); expect(w.text()).toContain('Reviewed');
    await w.get('a').trigger('click'); await flushPromises(); expect(w.text()).toContain('ENG-2'); expect((w.get('#linear-query').element as HTMLInputElement).value).toBe('resource');
  });
  it('creates issues with discovered workflow states and links uncertain writes without replay', async () => {
    const f = fixture(); const w = await render({ ...f.ctx, subPath: 'create/issue' });
    await choose(w, '#linear-connection', 'linear'); await choose(w, '#linear-team', 'Engineering'); await choose(w, '#issue-state', 'Done');
    await w.get('#issue-title').setValue('New issue'); f.setUncertain(); await w.get('form').trigger('submit'); await flushPromises();
    const creates = f.calls.filter(c => c.body?.spec.action === 'createIssue'); expect(creates).toHaveLength(1); expect(creates[0].body.spec.stateID).toBe('done');
    expect(w.text()).not.toContain('Issue creation succeeded');
    const link = w.findAll('a').find(a => a.text() === 'Inspect operation')!; expect(link.attributes('href')).toContain(creates[0].body.metadata.name);
    await link.trigger('click'); await flushPromises(); expect(w.text()).toContain('Uncertain');
    expect(f.calls.filter(c => c.body?.spec.action === 'createIssue')).toHaveLength(1);
  });
  it('keeps stale rows on refresh failure and clears namespace and workspace data', async () => {
    const f = fixture(); const w = await render(f.ctx); f.setFail(); await click(w, 'Refresh');
    expect(w.text()).toContain('linear'); expect(w.text()).toContain('503');
    await w.get('#linear-namespace').setValue('another'); await w.get('form').trigger('submit'); await flushPromises();
    expect(w.findAll('button.k-table-resource-link')).toHaveLength(0); expect(f.calls.at(-1)!.path).toContain('/namespaces/another/');
    await w.setProps({ ctx: { ...f.ctx, tenant: '' } }); await flushPromises(); expect(w.text()).toBe('Select a workspace to use Linear.');
  });
  it('fences late responses from a previous user even if transport ignores cancellation', async () => {
    let resolve!: (r: Response) => void;
    const w = await render({ tenant: 'one', user: { sub: 'old' }, fetch: () => new Promise(r => { resolve = r; }) });
    await w.setProps({ ctx: { tenant: 'one', user: { sub: 'new' }, fetch: async () => Response.json({ items: [] }) } }); await flushPromises();
    resolve(Response.json({ items: [{ metadata: { name: 'private-old-resource' } }] })); await flushPromises();
    expect(w.text()).not.toContain('private-old-resource'); expect(w.text()).toContain('Connect Linear');
  });
});

it('edits populated values, explicitly clears descriptions, and presents comment context', async () => {
  const f = fixture(); const w = await render({ ...f.ctx, subPath: issuePath('linear', 'issue') });
  expect((w.get('#issue-description').element as HTMLTextAreaElement).value).toBe(issue.description);
  await w.get('#issue-description').setValue(''); await w.findAll('form')[0].trigger('submit'); await flushPromises();
  const sent = f.calls.find(c => c.body?.spec.action === 'updateIssue')!.body.spec;
  expect(sent.description).toBe(''); expect(sent).not.toHaveProperty('title');
  expect(w.text()).toContain('No description.');
  await click(w, 'Read comments'); expect(w.text()).toContain('Reviewer'); expect(w.text()).toContain('Edited'); expect(w.text()).toContain('Reply to comment parent');
  expect(w.findAll('a').find(a => a.text() === 'View in Linear')!.attributes('rel')).toContain('noopener');
});
it('retains the issue draft across connection setup and fences it on namespace change', async () => {
  const f = fixture(); const w = await render({ ...f.ctx, subPath: 'create/issue' });
  await w.get('#issue-title').setValue('Draft to keep'); await w.get('#issue-description').setValue('Details to keep');
  await w.setProps({ ctx: { ...f.ctx, subPath: 'create/connection' } }); await flushPromises();
  await click(w, 'Return to issue draft');
  expect((w.get('#issue-title').element as HTMLInputElement).value).toBe('Draft to keep');
  expect((w.get('#issue-description').element as HTMLTextAreaElement).value).toBe('Details to keep');
  await w.setProps({ ctx: { ...f.ctx, subPath: 'issues/namespaces/other/create' } }); await flushPromises();
  expect((w.get('#issue-title').element as HTMLInputElement).value).toBe('');
});

it('does not interpret empty comma-separated team IDs as permission to allow all teams', async () => {
  const f = fixture(); const w = await render({ ...f.ctx, subPath: 'create/connection' });
  await w.get('#connection-name').setValue('scoped'); await w.get('#connection-secret').setValue('key'); await w.get('#connection-teams').setValue(' , , ');
  await w.get('form').trigger('submit'); await flushPromises();
  expect(f.calls.filter(c => c.body?.kind === 'Connection')).toHaveLength(0);
});
it('loads bounded history pages through shared navigation without eagerly following continuation', async () => {
  const paths: string[] = [];
  const w = await render({ tenant: 'workspace', subPath: 'operations', fetch: async input => {
    const path = String(input); paths.push(path);
    return Response.json({ items: [{ metadata: { name: path.includes('continue=') ? 'op-second' : 'op-first' }, status: { phase: 'Uncertain', message: 'Inspect before retrying' } }], metadata: { continue: path.includes('continue=') ? '' : 'next/cursor' } });
  } });
  expect(paths).toHaveLength(1); expect(paths[0]).toContain('limit=10'); expect(w.text()).toContain('op-first');
  await w.get('button[aria-label="Next page"]').trigger('click'); await flushPromises();
  expect(paths).toHaveLength(2); expect(paths[1]).toContain('continue=next%2Fcursor'); expect(w.text()).toContain('op-second');
  await w.get('button[aria-label="Previous page"]').trigger('click'); await flushPromises();
  expect(paths).toHaveLength(3); expect(paths[2]).not.toContain('continue='); expect(w.text()).toContain('op-first');
});
