import { afterEach, expect, it } from 'vitest';
import { webcrypto } from 'node:crypto';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { API, type FarosContext } from './api';
Object.defineProperty(globalThis.crypto, 'subtle', { value: webcrypto.subtle, configurable: true });
const wrappers: VueWrapper[] = [];
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); document.body.innerHTML = ''; });
function fixture() {
  const connection: any = { metadata: { name: 'main', uid: 'connection-uid', resourceVersion: '1' }, spec: {}, status: { ready: true } };
  const teams = new Map<string, any>(); const operations = new Map<string, any>(); const calls: { path: string; method?: string; body: any }[] = [];
  const fetch: FarosContext['fetch'] = async (input, init) => {
    const path = String(input), method = init?.method; const body = init?.body ? JSON.parse(String(init.body)) : undefined; calls.push({ path, method, body });
    if (path.includes('/api/connections/main/teams')) return Response.json({ nodes: [{ id: path.includes('after=') ? 'design' : 'eng', name: path.includes('after=') ? 'Design' : 'Engineering', key: path.includes('after=') ? 'DSN' : 'ENG' }], pageInfo: { hasNextPage: !path.includes('after='), endCursor: 'next' } });
    if (body?.kind === 'Team') {
      if (teams.has(body.metadata.name)) return new Response('', { status: 409 });
      const team = { ...body, metadata: { ...body.metadata, uid: 'team-uid' }, status: { ready: true, name: body.spec.teamID === 'eng' ? 'Engineering' : 'Design', key: 'ENG' } }; teams.set(team.metadata.name, team); return Response.json(team);
    }
    if (path.includes('/teams/')) { const name = path.split('/').pop()!; if (method === 'DELETE') { teams.delete(name); return new Response(null, { status: 204 }); } return Response.json(teams.get(name)); }
    if (path.includes('/teams?')) return Response.json({ items: [...teams.values()] });
    if (path.includes('/connections/')) return Response.json(connection);
    if (path.includes('/connections?')) return Response.json({ items: [connection] });
    if (body?.kind === 'Operation') { operations.set(body.metadata.name, body); return Response.json(body); }
    if (path.includes('/operations/')) { const spec = operations.get(path.split('/').pop()!)?.spec; const issue = { id: 'issue', identifier: 'ENG-1', title: 'Team issue', team: { id: 'eng' } }; return Response.json({ status: { phase: 'Succeeded', result: spec?.action === 'issue' ? issue : { nodes: spec?.action === 'issues' ? [issue] : [] } } }); }
    return Response.json({ items: [] });
  };
  return { ctx: { tenant: 'workspace', user: { sub: 'user' }, fetch } as FarosContext, teams, connection, calls };
}
async function render(ctx: FarosContext) {
  const w = mount(App, { props: { ctx }, attachTo: document.body }); wrappers.push(w);
  w.element.addEventListener('faros-navigate', (e: Event) => { void w.setProps({ ctx: { ...w.props('ctx'), subPath: (e as CustomEvent).detail.path } }); });
  await flushPromises(); return w;
}
async function click(w: VueWrapper, label: string) { const button = w.findAll('button').find(b => b.text().trim() === label); expect(button, label).toBeTruthy(); await button!.trigger('click'); await flushPromises(); }
async function settle() { for (let i = 0; i < 8; i++) await flushPromises(); }

it('registers selected existing teams with pinned ownership and opens their issues', async () => {
  const f = fixture(); const w = await render({ ...f.ctx, subPath: 'teams/create' });
  expect(w.text()).toContain('Engineering (ENG)');
  await w.get('form').trigger('submit'); await flushPromises(); expect(f.teams.size).toBe(0);
  await w.get('input[value="eng"]').setValue(true); await click(w, 'Load more teams'); expect(w.text()).toContain('Design (DSN)');
  await w.get('#team-search').setValue('Design'); expect(w.find('input[value="eng"]').exists()).toBe(false);
  await w.get('form').trigger('submit'); await settle();
  expect(f.teams.size).toBe(1); const team = [...f.teams.values()][0];
  expect(team.spec).toEqual({ connection: 'main', connectionUID: 'connection-uid', teamID: 'eng' });
  expect(team.metadata.ownerReferences[0].uid).toBe('connection-uid');
  expect(w.text()).toContain('ENG-1'); expect(w.find('#linear-connection').exists()).toBe(false);
  const issueRead = f.calls.find(call => call.body?.spec?.action === 'issues'); expect(issueRead?.body.spec).toMatchObject({ connection: 'main', teamID: 'eng' });
  await w.get('#linear-query').setValue('Team'); await w.get('form.linear-fields').trigger('submit'); await settle();
  await click(w, 'ENG-1'); await w.get('a').trigger('click'); await settle();
  expect((w.get('#linear-query').element as HTMLInputElement).value).toBe('Team'); expect(f.calls.filter(c => c.body?.spec?.action === 'issues').at(-1)?.body.spec.query).toBe('Team');
  await click(w, 'Create issue'); expect(w.find('#linear-team').exists()).toBe(false); expect(w.text()).toContain('Creating in the selected Team');
  await click(w, 'Cancel'); expect(w.props('ctx')!.subPath).toBe('teams/detail/' + team.metadata.name);
});

it('reuses the same registration identity after a duplicate create without writing to Linear', async () => {
  const f = fixture(); const api = new API(f.ctx, new AbortController().signal);
  const first = await api.addTeam(f.connection, 'eng'); const again = await api.addTeam(f.connection, 'eng');
  expect(again.metadata.uid).toBe(first.metadata.uid); expect(f.teams.size).toBe(1);
  expect(f.calls.every(c => !c.path.includes('/operations'))).toBe(true);
});

it('removes a Team registration only, leaving its Connection and upstream issue untouched', async () => {
  const f = fixture(); const api = new API(f.ctx, new AbortController().signal); const team = await api.addTeam(f.connection, 'eng');
  const w = await render({ ...f.ctx, subPath: 'teams/detail/' + team.metadata.name });
  await w.get('button[aria-label="More team actions"]').trigger('click'); await flushPromises();
  (document.querySelector('[role="menuitem"]') as HTMLElement).click(); await flushPromises();
  expect(document.body.textContent).toContain('The team and its issues remain in Linear');
  ([...document.querySelectorAll('[role="alertdialog"] button')].find(b => b.textContent?.trim() === 'Remove') as HTMLElement).click(); await settle();
  const deletes = f.calls.filter(c => c.method === 'DELETE'); expect(deletes).toHaveLength(1); expect(deletes[0].path).toContain('/teams/'); expect(deletes[0].body.preconditions.uid).toBe('team-uid');
  expect(w.props('ctx')!.subPath).toBe('teams'); expect(f.connection.metadata.uid).toBe('connection-uid');
});


it('marks existing registrations without any policy switch or consent', async () => {
  const f = fixture(); await new API(f.ctx, new AbortController().signal).addTeam(f.connection, 'design');
  const w = await render({ ...f.ctx, subPath: 'teams/create' });
  await click(w, 'Load more teams');
  expect((w.get('input[value="design"]').element as HTMLInputElement).checked).toBe(true);
  expect(w.get('input[value="design"]').attributes('disabled')).toBeDefined();
  expect(w.text()).not.toMatch(/migrat|legacy|Replace this Connection/i);
  await w.get('input[value="eng"]').setValue(true); await w.get('form').trigger('submit'); await settle();
  expect(f.teams.size).toBe(2); expect(f.calls.some(c => c.method === 'PATCH')).toBe(false);
});
