import { afterEach, describe, expect, it } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import type { FarosContext, Resource } from './api';
import { canonicalPath, issuePath, parseRoute, resourcePath } from './routes';

type Navigation = { path: string; replace: boolean };
type Operation = { metadata: { name: string }; spec: Record<string, any> };

const wrappers: VueWrapper[] = [];

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount());
  document.body.innerHTML = '';
});

const firstIssue = {
  id: 'issue-1',
  identifier: 'ENG-1',
  title: 'Ship resource pages',
  description: 'Original description',
  team: { id: 'team', name: 'Engineering' },
  state: { id: 'todo', name: 'Todo' },
};

const secondIssue = {
  id: 'issue-2',
  identifier: 'ENG-2',
  title: 'Second resource',
  description: 'Second description',
  team: { id: 'team', name: 'Engineering' },
  state: { id: 'todo', name: 'Todo' },
};

function connection(name: string): Resource {
  return {
    metadata: { name, uid: name + '-uid', resourceVersion: '1' },
    spec: { apiKeySecretRef: { name: `${name}-key` } },
    status: { ready: true },
  };
}

function fixture(initialConnections: Resource[] = [connection('linear')]) {
  const calls: { path: string; body?: any }[] = [];
  const operations = new Map<string, Operation>();
  let connections = [...initialConnections];
  let issue = { ...firstIssue };
  let pageTwoIssue = { ...secondIssue };
  let issueList = [issue];
  let failIssueRead = false;
  let failIssueList = false;
  let delayMutation = false;
  let mutationReadStarted = false;
  let releaseMutationRead: (() => void) | undefined;

  function operationResult(operation: Operation): Record<string, unknown> {
    const spec = operation.spec;
    if (spec.action === 'teams') return { nodes: [{ id: 'team', name: 'Engineering', key: 'ENG' }] };
    if (spec.action === 'states') return { nodes: [{ id: 'todo', name: 'Todo' }, { id: 'done', name: 'Done' }] };
    if (spec.action === 'issues') {
      if (failIssueList) return { __httpError: 503 };
      if (spec.query === 'resource') {
        return {
          nodes: [spec.after ? pageTwoIssue : issue],
          pageInfo: { hasNextPage: !spec.after, endCursor: spec.after ? '' : 'next' },
        };
      }
      return { nodes: issueList, pageInfo: { hasNextPage: false, endCursor: '' } };
    }
    if (spec.action === 'issue') return spec.issueID === pageTwoIssue.id ? pageTwoIssue : issue;
    if (spec.action === 'updateIssue') {
      const updated = { ...(spec.issueID === pageTwoIssue.id ? pageTwoIssue : issue), ...(spec.title ? { title: spec.title } : {}) };
      if (spec.issueID === pageTwoIssue.id) pageTwoIssue = updated;
      else {
        issue = updated;
        issueList = issueList.map(candidate => candidate.id === updated.id ? updated : candidate);
      }
      return updated;
    }
    if (spec.action === 'createIssue') {
      pageTwoIssue = { ...pageTwoIssue, id: 'issue-2', identifier: 'ENG-2', title: spec.title };
      issueList = [...issueList, pageTwoIssue];
      return pageTwoIssue;
    }
    if (spec.action === 'comments') return { nodes: [{ id: 'comment', body: 'Reviewed' }] };
    return {};
  }

  const ctx: FarosContext = {
    tenant: 'workspace',
    user: { sub: 'reviewer' },
    subPath: '',
    fetch: async (input, init) => {
      const path = String(input);
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, body });

      if (path.includes('/teams')) return Response.json({ items: [], nodes: [{ id: 'team', name: 'Engineering' }] });
      if (path.endsWith('/onboarding/teams')) return Response.json({ nodes: [{ id: 'team', name: 'Engineering' }] });
      if (body?.kind === 'Secret') return Response.json({ metadata: body.metadata });
      if (body?.kind === 'Connection') {
        const created = { ...body, metadata: { ...body.metadata, uid: 'uid' }, status: { ready: true } } as Resource;
        connections = [...connections, created];
        return Response.json(created);
      }
      if (body?.kind === 'Operation') {
        operations.set(body.metadata.name, body as Operation);
        return Response.json(body);
      }

      const name = path.split('/').pop()!;
      if (path.includes('/operations/')) {
        const operation = operations.get(name)!;
        const spec = operation.spec;
        if (spec.action === 'issue' && failIssueRead) {
          failIssueRead = false;
          return new Response('', { status: 503 });
        }
        if (spec.action === 'issues' && failIssueList) return new Response('', { status: 503 });
        if (['createIssue', 'updateIssue', 'addComment'].includes(spec.action) && delayMutation) {
          mutationReadStarted = true;
          return new Promise<Response>(resolve => { releaseMutationRead = () => { delayMutation = false; resolve(Response.json({ ...operation, status: { phase: 'Succeeded', result: operationResult(operation) } })); }; });
        }
        const result = operationResult(operation);
        if (result.__httpError) return new Response('', { status: Number(result.__httpError) });
        return Response.json({ ...operation, status: { phase: 'Succeeded', result } });
      }
      if (path.includes('/connections?')) return Response.json({ items: connections });
      if (path.includes('/connections/')) return Response.json(connections.find(item => item.metadata.name === name) || connection(name));
      return Response.json({ items: [] });
    },
  };

  return {
    ctx,
    calls,
    failNextIssueRead: () => { failIssueRead = true; },
    failIssueList: () => { failIssueList = true; },
    delayNextMutation: () => { delayMutation = true; },
    releaseMutation: () => { releaseMutationRead?.(); },
    get mutationReadStarted() { return mutationReadStarted; },
  };
}

async function render(ctx: FarosContext, navigations: Navigation[] = []): Promise<VueWrapper> {
  const wrapper = mount(App, { props: { ctx }, attachTo: document.body });
  wrappers.push(wrapper);
  wrapper.element.addEventListener('faros-navigate', (event: Event) => {
    const detail = (event as CustomEvent<{ path?: unknown; replace?: unknown }>).detail;
    if (typeof detail?.path !== 'string') throw new Error('host navigation omitted path');
    navigations.push({ path: detail.path, replace: detail.replace === true });
    void wrapper.setProps({ ctx: { ...wrapper.props('ctx'), subPath: detail.path } });
  });
  await flushPromises();
  return wrapper;
}

async function clickText(wrapper: VueWrapper, text: string): Promise<void> {
  const target = wrapper.findAll('button, a').find(candidate => candidate.text().trim() === text || candidate.attributes('aria-label') === `${text} page`);
  expect(target, `control ${text}`).toBeTruthy();
  await target!.trigger('click');
  await flushPromises();
}

async function choose(wrapper: VueWrapper, id: string, label: string): Promise<void> {
  await wrapper.get(id).trigger('click');
  await flushPromises();
  const option = [...document.querySelectorAll('[role="option"]')]
    .find(element => element.textContent?.includes(label));
  expect(option, `option ${label}`).toBeTruthy();
  (option as HTMLElement).click();
  await flushPromises();
}

async function searchIssues(wrapper: VueWrapper, query = ''): Promise<void> {
  if (query) await wrapper.get('#linear-query').setValue(query);
  await wrapper.get('form.linear-fields').trigger('submit');
  await flushPromises();
}

describe('Linear canonical portal regressions', () => {
  it.each(['connections', 'connections/detail/main'])('confirms deletion and refreshes the collection from %s', async subPath => {
    let exists = true; let deletes = 0;
    const item = { metadata: { name: 'main', uid: 'uid' }, spec: {}, status: {} };
    const wrapper = await render({ tenant: 'workspace', subPath, fetch: async (path, init) => {
      if (init?.method === 'DELETE') { deletes++; exists = false; return new Response(null, { status: 204 }); }
      return Response.json(String(path).includes('/connections/') ? item : { items: exists ? [item] : [] });
    } });
    async function openDelete() {
      if (subPath.includes('/detail/')) {
        await wrapper.get('button[aria-label="More connection actions"]').trigger('click'); await flushPromises();
        (document.querySelector('[role="menuitem"]') as HTMLElement).click();
      } else await wrapper.get('button[aria-label="Delete connection main"]').trigger('click');
      await flushPromises();
    }
    await openDelete();
    expect(document.body.textContent).toContain('Delete connection "main"?'); expect(deletes).toBe(0);
    const dialogButton = (text: string) => [...document.querySelectorAll('[role="alertdialog"] button')].find(button => button.textContent?.trim() === text) as HTMLElement;
    dialogButton('Cancel').click(); await flushPromises(); expect(deletes).toBe(0);
    await openDelete(); dialogButton('Delete').click(); await flushPromises();
    expect(deletes).toBe(1); expect(wrapper.text()).toContain('Connect a Linear account');
  });
  it('keeps the connection visible when deletion is denied', async () => {
    const wrapper = await render({ tenant: 'workspace', subPath: 'connections', fetch: async (_path, init) => init?.method === 'DELETE' ? new Response('', { status: 403 }) : Response.json({ items: [{ metadata: { name: 'main', uid: 'uid' } }] }) });
    await wrapper.get('button[aria-label="Delete connection main"]').trigger('click'); await flushPromises();
    ([...document.querySelectorAll('[role="alertdialog"] button')].find(button => button.textContent?.trim() === 'Delete') as HTMLElement).click(); await flushPromises();
    expect(wrapper.text()).toContain('Ask your workspace administrator');
    expect(wrapper.get('button[aria-label="Delete connection main"]').attributes('disabled')).toBeUndefined();
  });
  it('shows one actionable onboarding guide and no issue controls without connections', async () => {
    const f = fixture([]);
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });
    expect(wrapper.text()).toContain('Create a connection first');
    expect(wrapper.find('#linear-connection').exists()).toBe(false);
    expect(wrapper.find('#linear-query').exists()).toBe(false);
    expect(wrapper.findAll('button').some(button => button.text() === 'Create issue')).toBe(false);
    await wrapper.setProps({ ctx: { ...f.ctx, subPath: 'connections' } }); await flushPromises();
    expect(wrapper.text()).toContain('Connect a Linear account');
    expect(wrapper.findAll('button').filter(button => button.text().includes('Add connection'))).toHaveLength(1);
    expect(wrapper.text()).toContain('Choose which teams to allow');
  });

  it('does not present an authorization failure as first-run onboarding', async () => {
    const wrapper = await render({ tenant: 'workspace', subPath: 'issues', fetch: async () => new Response('', { status: 403 }) });
    expect(wrapper.text()).toContain('Ask your workspace administrator');
    expect(wrapper.text()).not.toContain('Create a connection first');
    expect(wrapper.find('#linear-query').exists()).toBe(false);
  });

  it('checks a key without persisting it and creates a connection with no registered team access', async () => {
    const calls: { path: string; body: any }[] = [];
    const ctx: FarosContext = { tenant: 'workspace', subPath: 'connections/create', fetch: async (input, init) => {
      const path = String(input); const body = init?.body ? JSON.parse(String(init.body)) : undefined; calls.push({ path, body });
      if (path.endsWith('/onboarding/teams')) return Response.json({ nodes: [{ id: body.after ? 'design' : 'engineering', name: body.after ? 'Design' : 'Engineering', key: body.after ? 'DSN' : 'ENG' }], pageInfo: { hasNextPage: !body.after, endCursor: 'next' } });
      if (body?.kind === 'Connection') return Response.json({ ...body, metadata: { ...body.metadata, uid: 'uid' } });
      if (body?.kind === 'Secret') return Response.json({ metadata: body.metadata });
      return Response.json({ items: [] });
    } };
    const wrapper = await render(ctx);
    await wrapper.get('#connection-name').setValue('new'); await wrapper.get('#connection-api-key').setValue('private-key');
    await clickText(wrapper, 'Check API key');
    expect(calls).toHaveLength(1); expect(calls[0].path).toContain('/onboarding/teams');
    expect(wrapper.get('#connection-api-key').attributes('type')).toBe('password');
    expect(wrapper.find('fieldset').exists()).toBe(false);
    await wrapper.get('form').trigger('submit'); await flushPromises();
    const connection = calls.find(call => call.body?.kind === 'Connection')!.body;
    expect(connection.spec).not.toHaveProperty('teams'); expect(connection.spec).not.toHaveProperty('teamPolicy'); expect(JSON.stringify(connection)).not.toContain('private-key');
    expect(calls.find(call => call.body?.kind === 'Secret')!.body.stringData.apiKey).toBe('private-key');
    expect(wrapper.find('#connection-api-key').exists()).toBe(false);
  });

  it('requires revalidation immediately when the API key changes', async () => {
    const wrapper = await render({ tenant: 'workspace', subPath: 'connections/create', fetch: async () => Response.json({ nodes: [{ id: 'team', name: 'Engineering' }] }) });
    await wrapper.get('#connection-api-key').setValue('first-key'); await clickText(wrapper, 'Check API key');

    await wrapper.get('#connection-api-key').setValue('replacement-key');
    expect(wrapper.find('input[value="team"]').exists()).toBe(false);
    expect(wrapper.findAll('button').find(button => button.text() === 'Add connection')!.attributes('disabled')).toBeDefined();
  });

  it('fences an old key discovery response even when transport ignores cancellation', async () => {
    let resolve!: (response: Response) => void;
    const wrapper = await render({ tenant: 'workspace', subPath: 'connections/create', fetch: () => new Promise<Response>(done => { resolve = done; }) });
    await wrapper.get('#connection-api-key').setValue('old-key'); await clickText(wrapper, 'Check API key');
    await wrapper.get('#connection-api-key').setValue('new-key');
    resolve(Response.json({ nodes: [{ id: 'old-team', name: 'Old private team' }] })); await flushPromises();
    expect(wrapper.text()).not.toContain('Old private team');
    expect(wrapper.findAll('button').find(button => button.text() === 'Add connection')!.attributes('disabled')).toBeDefined();
  });

  it.each([false, true])('keeps settings stable only after a partial save (partial=%s)', async partial => {
    const wrapper = await render({ tenant: 'workspace', subPath: 'connections/create', fetch: async (_path, init) => {
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      if (body?.apiKey) return Response.json({ nodes: [{ id: 'team', name: 'Engineering' }] });
      if (body?.kind === 'Connection') return partial ? Response.json({ ...body, metadata: { ...body.metadata, uid: 'uid' } }) : new Response('', { status: 422 });
      return new Response('', { status: 403 });
    } });
    await wrapper.get('#connection-name').setValue('engineering'); await wrapper.get('#connection-api-key').setValue('key');
    await clickText(wrapper, 'Check API key');
    await wrapper.get('form').trigger('submit'); await flushPromises();
    expect((wrapper.get('#connection-name').element as HTMLInputElement).disabled).toBe(partial);
    expect((wrapper.get('#connection-api-key').element as HTMLInputElement).value).toBe('key');
    if (partial) expect(wrapper.text()).toContain('credential storage could not be confirmed');
  });

  it('keeps comment progress, recovery and completion beside its composer', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues/detail/linear/issue-1' });
    f.delayNextMutation();
    await wrapper.get('#issue-comment').setValue('Review this change');
    const comments = wrapper.findAll('section').find(section => section.find('#issue-comment').exists())!;
    const form = wrapper.findAll('form').find(item => item.find('#issue-comment').exists())!;
    await form.trigger('submit'); await flushPromises();
    expect(f.mutationReadStarted).toBe(true);
    expect(form.text()).toContain('Adding…');
    expect(wrapper.findAll('button').find(button => button.text() === 'Refresh')!.attributes()).toHaveProperty('disabled');
    expect(wrapper.findAll('button').find(button => button.text() === 'Update issue')).toBeTruthy();
    f.releaseMutation(); await flushPromises();
    expect(comments.text()).toContain('Comment added.');
    expect((wrapper.get('#issue-comment').element as HTMLTextAreaElement).value).toBe('');
    expect(f.calls.filter(call => call.body?.spec?.action === 'addComment')).toHaveLength(1);
  });

  it('opens Team registration directly without a Connection policy form', async () => {
    const f = fixture(); const wrapper = await render({ ...f.ctx, subPath: 'connections/detail/linear' });
    expect(wrapper.find('#policy-teams').exists()).toBe(false);
    expect(wrapper.text()).toContain('Registered Teams only');
    await clickText(wrapper, 'Add teams');
    expect(wrapper.text()).not.toMatch(/Replace this Connection|legacy|migrat/i);
  });

  it('preserves edits on refresh and allows explicitly discarding them without a write', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues/detail/linear/issue-1' });
    const original = (wrapper.get('#issue-title').element as HTMLInputElement).value;
    await wrapper.get('#issue-title').setValue('Unsaved title');
    await wrapper.get('#issue-description').setValue('Unsaved description');
    await choose(wrapper, '#issue-state', 'Done');
    await clickText(wrapper, 'Refresh');
    expect((wrapper.get('#issue-title').element as HTMLInputElement).value).toBe('Unsaved title');
    expect((wrapper.get('#issue-description').element as HTMLTextAreaElement).value).toBe('Unsaved description');
    expect(wrapper.get('#issue-state').text()).toContain('Done');
    expect(wrapper.text()).toContain('Unsaved changes.');
    await clickText(wrapper, 'Discard changes');
    expect((wrapper.get('#issue-title').element as HTMLInputElement).value).toBe(original);
    expect(wrapper.get('#issue-state').text()).toContain('Keep current state');
    expect(f.calls.filter(call => call.body?.spec?.action === 'updateIssue')).toHaveLength(0);
  });

  it('keeps applied search and pagination stable while editing, and clears search explicitly', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper, 'resource');
    await wrapper.get('#linear-query').setValue('draft query');
    expect(wrapper.text()).toContain('ENG-1');
    expect(wrapper.text()).toContain('Search to apply your changes.');
    await clickText(wrapper, 'Next');
    expect(f.calls.filter(call => call.body?.spec?.action === 'issues').at(-1)?.body.spec).toMatchObject({ query: 'resource', after: 'next' });
    await clickText(wrapper, 'Clear search');
    expect((wrapper.get('#linear-query').element as HTMLInputElement).value).toBe('');
    expect(f.calls.filter(call => call.body?.spec?.action === 'issues').at(-1)?.body.spec).toMatchObject({ query: '' });
    expect(f.calls.filter(call => call.body?.spec?.action === 'issues').at(-1)?.body.spec).not.toHaveProperty('after');
    f.failIssueList();
    await searchIssues(wrapper, 'failed query');
    await wrapper.get('#linear-query').setValue('not submitted');
    await clickText(wrapper, 'Retry');
    expect(f.calls.filter(call => call.body?.spec?.action === 'issues').at(-1)?.body.spec.query).toBe('failed query');
  });

  it('uses workspace routes and treats reserved words as explicit resource identities', () => {
    for (const name of ['create', 'detail', 'namespaces', 'conn/name']) {
      expect(parseRoute(resourcePath('connections', name))).toEqual({ page: 'connections', name });
      expect(parseRoute(issuePath(name, 'issue/id'))).toEqual({ page: 'issues', connection: name, name: 'issue/id' });
    }
    expect(parseRoute('connections/create')).toEqual({ page: 'connections', create: 'connection' });
    expect(parseRoute('issues/create')).toEqual({ page: 'issues', create: 'issue' });
    expect(parseRoute('connections/namespaces/default').invalid).toBe(true);
    expect(parseRoute('namespaces/default/connections').invalid).toBe(true);
    expect(canonicalPath(resourcePath('connections', 'namespaces'))).toBe('connections/detail/namespaces');
  });

  it('uses canonical host navigation for create, detail, issue detail, and Back', async () => {
    const f = fixture();
    const navigations: Navigation[] = [];
    const wrapper = await render({ ...f.ctx, subPath: 'connections' }, navigations);

    await clickText(wrapper, 'Add connection');
    expect(navigations.at(-1)).toEqual({ path: 'connections/create', replace: false });
    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();
    expect(navigations.at(-1)).toEqual({ path: 'connections', replace: false });

    await clickText(wrapper, 'linear');
    expect(navigations.at(-1)).toEqual({ path: 'connections/detail/linear', replace: false });

    await wrapper.setProps({ ctx: { ...f.ctx, subPath: 'issues' } });
    await flushPromises();
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper);
    await clickText(wrapper, 'ENG-1');
    expect(navigations.at(-1)).toEqual({ path: 'issues/detail/linear/issue-1', replace: false });

    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();
    await clickText(wrapper, 'Create issue');
    expect(navigations.at(-1)).toEqual({ path: 'issues/create', replace: false });
  });

  it('refreshes the cached scope after creating a connection and browsing from its detail', async () => {
    const f = fixture([]);
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });

    await clickText(wrapper, 'Add connection');
    await wrapper.get('#connection-name').setValue('new-connection');
    await wrapper.get('#connection-api-key').setValue('fixture-key');
    await clickText(wrapper, 'Check API key');

    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Add teams');
    expect(wrapper.get('#team-connection').text()).toContain('new-connection');
  });

  it('refreshes cached issues while preserving the query and page after a detail update', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper, 'resource');
    await clickText(wrapper, 'Next');
    await clickText(wrapper, 'ENG-2');
    await wrapper.get('#issue-title').setValue('Updated title');
    const updateForm = wrapper.findAll('form').find(form => form.find('#issue-title').exists());
    expect(updateForm).toBeTruthy();
    await updateForm!.trigger('submit');
    await flushPromises();
    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();

    expect({
      query: (wrapper.get('#linear-query').element as HTMLInputElement).value,
      page: wrapper.text().match(/Page \d+/)?.[0],
      title: wrapper.text().includes('Updated title'),
      lastIssueRead: f.calls.filter(call => call.body?.spec?.action === 'issues').at(-1)?.body?.spec,
    }).toMatchObject({
      query: 'resource',
      page: 'Page 2',
      title: true,
      lastIssueRead: expect.objectContaining({ after: 'next', query: 'resource' }),
    });
  });

  it('refreshes cached issues after creation before returning from the new detail', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper);
    await clickText(wrapper, 'Create issue');
    await wrapper.get('#issue-title').setValue('New issue');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();

    expect(wrapper.text()).toContain('ENG-2');
    expect(wrapper.text()).toContain('New issue');
  });

  it('returns through host history without exposing a storage namespace selector', async () => {
    const f = fixture(); const navigations: Navigation[] = [];
    const wrapper = await render({ ...f.ctx, subPath: 'connections' }, navigations);
    expect(wrapper.find('#linear-namespace').exists()).toBe(false);
    await clickText(wrapper, 'Teams'); expect(navigations.at(-1)?.path).toBe('teams');
    await wrapper.setProps({ ctx: { ...f.ctx, subPath: 'connections' } }); await flushPromises();
    expect(f.calls.filter(call => call.path.includes('/connections?')).at(-1)?.path).toBe('/clusters/workspace/apis/linear.providers.faros.sh/v1alpha1/connections?limit=100');
  });

  it('keeps the last issue page visible when activation refresh fails', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues' });
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper);
    f.failIssueList();

    await clickText(wrapper, 'ENG-1');
    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();

    expect(wrapper.text()).toContain('ENG-1');
    expect(wrapper.text()).toContain('503');
    expect(wrapper.text()).toContain('Showing the last successful result.');
  });

  it('does not expose a detail Retry action that cannot run during a delayed mutation', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues/detail/linear/issue-1' });
    f.failNextIssueRead();
    await clickText(wrapper, 'Refresh');
    expect(wrapper.find('.k-resource-page__retry').exists()).toBe(true);

    f.delayNextMutation();
    await wrapper.get('#issue-title').setValue('Retry-safe title');
    const updateForm = wrapper.findAll('form').find(form => form.find('#issue-title').exists());
    expect(updateForm).toBeTruthy();
    await updateForm!.trigger('submit');
    await flushPromises();
    expect(f.mutationReadStarted).toBe(true);

    expect(wrapper.find('.k-resource-page__retry').exists()).toBe(false);
    f.releaseMutation();
    await flushPromises();
    const retry = wrapper.get('.k-resource-page__retry');
    expect((retry.element as HTMLButtonElement).disabled).toBe(false);
    const issueReadsBeforeRetry = f.calls.filter(call => call.body?.spec?.action === 'issue').length;
    await retry.trigger('click');
    await flushPromises();
    expect(f.calls.filter(call => call.body?.spec?.action === 'issue').length).toBeGreaterThan(issueReadsBeforeRetry);
  });
});


it('recovers creation after navigation without a second submission or changing connection authority', async () => {
  const f = fixture([connection('linear'), connection('other')]);
  f.ctx.subPath = 'issues/create';
  const navigations: Navigation[] = [];
  const wrapper = await render(f.ctx, navigations);
  await choose(wrapper, '#linear-connection', 'linear');
  await choose(wrapper, '#linear-team', 'Engineering');
  await wrapper.get('#issue-title').setValue('Keep this intent');
  f.delayNextMutation();
  await wrapper.get('form.k-create-surface').trigger('submit'); await flushPromises();
  expect(f.mutationReadStarted).toBe(true);
  await clickText(wrapper, 'Back to issues');
  await choose(wrapper, '#linear-connection', 'other');
  await clickText(wrapper, 'Create issue');
  expect((wrapper.get('#issue-title').element as HTMLInputElement).value).toBe('Keep this intent');
  expect(wrapper.text()).toContain('Inspect submitted operation');
  expect(wrapper.findAll('button').find(b => b.text() === 'Create issue')!.attributes('disabled')).toBeDefined();
  await wrapper.get('form.k-create-surface').trigger('submit'); await flushPromises();
  expect(f.calls.filter(c => c.body?.spec?.action === 'createIssue')).toHaveLength(1);
  f.releaseMutation(); await flushPromises();
  await clickText(wrapper, 'Check outcome');
  expect(navigations.at(-1)?.path).toBe(issuePath('linear', 'issue-2'));
  expect(f.calls.filter(c => c.body?.spec?.action === 'createIssue')).toHaveLength(1);
});

it.each(['updateIssue', 'addComment'])('restores %s recovery on the issue detail and fences another submission', async action => {
  const f = fixture(); f.ctx.subPath = issuePath('linear', 'issue-1');
  const wrapper = await render(f.ctx);
  f.delayNextMutation();
  if (action === 'updateIssue') await wrapper.get('#issue-title').setValue('Updated title');
  else await wrapper.get('#issue-comment').setValue('One comment');
  const form = wrapper.findAll('form.linear-form')[action === 'updateIssue' ? 0 : 1];
  await form.trigger('submit'); await flushPromises();
  expect(f.mutationReadStarted).toBe(true);
  await clickText(wrapper, 'Back to issues');
  await wrapper.setProps({ ctx: { ...f.ctx, subPath: issuePath('linear', 'issue-1') } }); await flushPromises();
  expect(wrapper.text()).toContain('Inspect submitted operation');
  const button = wrapper.findAll('button').find(b => b.text() === (action === 'updateIssue' ? 'Update issue' : 'Add comment'))!;
  expect(button.attributes('disabled')).toBeDefined();
  f.releaseMutation(); await flushPromises(); await clickText(wrapper, 'Check outcome');
  expect(wrapper.text()).toContain(action === 'updateIssue' ? 'Issue updated.' : 'Comment added.');
  expect(f.calls.filter(c => c.body?.spec?.action === action)).toHaveLength(1);
});

it('requires explicit acknowledgment to prepare a separate intent and clears recovery on authority changes', async () => {
  const f = fixture(); f.ctx.subPath = 'issues/create';
  const wrapper = await render(f.ctx);
  await choose(wrapper, '#linear-connection', 'linear'); await choose(wrapper, '#linear-team', 'Engineering');
  await wrapper.get('#issue-title').setValue('An intentional new write');
  f.delayNextMutation(); await wrapper.get('form.k-create-surface').trigger('submit'); await flushPromises();
  await clickText(wrapper, 'Back to issues'); await clickText(wrapper, 'Create issue');
  await wrapper.get('summary').trigger('click');
  expect(f.calls.filter(c => c.body?.spec?.action === 'createIssue')).toHaveLength(1);
  expect(wrapper.findAll('button').find(b => b.text() === 'Create issue')!.attributes('disabled')).toBeDefined();
  await clickText(wrapper, 'I checked Linear; prepare a separate write');
  expect(wrapper.text()).not.toContain('Inspect submitted operation');
  await wrapper.get('form.k-create-surface').trigger('submit'); await flushPromises();
  const writes = f.calls.filter(c => c.body?.spec?.action === 'createIssue');
  expect(writes).toHaveLength(2); expect(writes[0].body.metadata.name).not.toBe(writes[1].body.metadata.name);
  await wrapper.setProps({ ctx: { ...f.ctx, user: { sub: 'another-user' } } }); await flushPromises();
  expect(wrapper.text()).not.toContain('Inspect submitted operation');
  expect((wrapper.get('#issue-title').element as HTMLInputElement).value).toBe('');
});
