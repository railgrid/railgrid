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
    metadata: { name },
    spec: { apiKeySecretRef: { name: `${name}-key` }, teams: [{ id: 'team' }] },
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

      if (body?.kind === 'Connection') {
        const created = { ...body, status: { ready: true } } as Resource;
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
        if (['updateIssue', 'addComment'].includes(spec.action) && delayMutation) {
          mutationReadStarted = true;
          return new Promise<Response>(resolve => { releaseMutationRead = () => resolve(Response.json({ ...operation, status: { phase: 'Succeeded', result: operationResult(operation) } })); });
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
  const target = wrapper.findAll('button, a').find(candidate => candidate.text().trim() === text);
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

  it('selects discovered policy teams while preserving manual IDs and announces a versioned save', async () => {
    const f = fixture();
    const conn = { ...connection('linear'), metadata: { name: 'linear', resourceVersion: '1' }, spec: { teams: [{ id: 'team' }, { id: 'manual' }] } };
    let patched: any;
    const fetch: FarosContext['fetch'] = async (input, init) => {
      if (init?.method === 'PATCH') {
        patched = JSON.parse(String(init.body));
        return Response.json({ ...conn, metadata: { name: 'linear', resourceVersion: '2' }, spec: { ...conn.spec, ...patched.spec } });
      }
      if (String(input).endsWith('/connections/linear')) return Response.json(conn);
      return f.ctx.fetch!(input, init);
    };
    const wrapper = await render({ ...f.ctx, fetch, subPath: 'connections/detail/linear' });
    await clickText(wrapper, 'Discover permitted teams');
    const checkbox = wrapper.get('fieldset input[type="checkbox"]');
    expect((checkbox.element as HTMLInputElement).checked).toBe(true);
    await checkbox.setValue(false);
    expect((wrapper.get('#policy-teams').element as HTMLInputElement).value).toBe('manual');
    await clickText(wrapper, 'Save team policy');
    expect(patched).toEqual({ metadata: { resourceVersion: '1' }, spec: { teams: [{ id: 'manual' }] } });
    expect(wrapper.text()).toContain('Team policy saved.');
    expect((wrapper.get('#policy-teams').element as HTMLInputElement).value).toBe('manual');
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
    await wrapper.get('#connection-secret').setValue('new-secret');
    await wrapper.get('#connection-teams').setValue('team');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    await clickText(wrapper, 'Browse issues');

    expect(wrapper.get('#linear-connection').text()).toContain('new-connection');
    await wrapper.get('#linear-connection').trigger('click');
    await flushPromises();
    expect([...document.querySelectorAll('[role="option"]')].map(element => element.textContent?.trim()))
      .toContain('new-connection');
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
    await clickText(wrapper, 'Issues'); expect(navigations.at(-1)?.path).toBe('issues');
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
