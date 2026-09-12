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
        if (spec.action === 'updateIssue' && delayMutation) {
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
  it('parses canonical routes and keeps reserved create/detail names addressable', () => {
    expect(parseRoute('connections/namespaces/default')).toMatchObject({ page: 'connections', namespace: 'default' });
    expect(parseRoute('connections/namespaces/default/detail/create')).toMatchObject({ page: 'connections', namespace: 'default', name: 'create' });
    expect(parseRoute('connections/namespaces/default/detail/detail')).toMatchObject({ page: 'connections', namespace: 'default', name: 'detail' });
    expect(parseRoute('connections/namespaces/default/detail/namespaces')).toMatchObject({ page: 'connections', namespace: 'default', name: 'namespaces' });
    expect(parseRoute('issues/namespaces/default/detail/create/detail')).toMatchObject({ page: 'issues', namespace: 'default', connection: 'create', name: 'detail' });
    expect(parseRoute('issues/namespaces/default/detail/namespaces/issue-1')).toMatchObject({ page: 'issues', namespace: 'default', connection: 'namespaces', name: 'issue-1' });
    expect(parseRoute('connections/namespaces/default/create')).toMatchObject({ page: 'connections', namespace: 'default', create: 'connection' });
    expect(parseRoute('issues/namespaces/default/create')).toMatchObject({ page: 'issues', namespace: 'default', create: 'issue' });
    expect(parseRoute('connections/namespaces')).toMatchObject({ page: 'connections', name: 'namespaces' });
    // The three-segment form reserves `namespaces/<namespace>` for the
    // canonical collection route; legacy issue identities use issuePath's
    // explicit detail marker when the connection is named "namespaces".
    expect(parseRoute('issues/namespaces/issue-1')).toMatchObject({ page: 'issues', namespace: 'issue-1' });
    expect(parseRoute('issues/namespaces/eng-1')).toMatchObject({ page: 'issues', namespace: 'eng-1' });
    expect(parseRoute(issuePath('namespaces', 'issue-1'))).toMatchObject({ page: 'issues', connection: 'namespaces', name: 'issue-1' });
    expect(parseRoute('namespaces/default/connections')).toMatchObject({ page: 'connections', namespace: 'default' });
    expect(parseRoute('connections')).toMatchObject({ page: 'connections' });
    expect(canonicalPath('default', resourcePath('connections', 'namespaces')))
      .toBe('connections/namespaces/default/detail/namespaces');
    expect(canonicalPath('default', issuePath('namespaces', 'default')))
      .toBe('issues/namespaces/default/detail/namespaces/default');
  });

  it('uses canonical host navigation for create, detail, issue detail, and Back', async () => {
    const f = fixture();
    const navigations: Navigation[] = [];
    const wrapper = await render({ ...f.ctx, subPath: 'connections/namespaces/default' }, navigations);

    await clickText(wrapper, 'Add connection');
    expect(navigations.at(-1)).toEqual({ path: 'connections/namespaces/default/create', replace: false });
    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();
    expect(navigations.at(-1)).toEqual({ path: 'connections/namespaces/default', replace: false });

    await clickText(wrapper, 'linear');
    expect(navigations.at(-1)).toEqual({ path: 'connections/namespaces/default/detail/linear', replace: false });

    await wrapper.setProps({ ctx: { ...f.ctx, subPath: 'issues/namespaces/default' } });
    await flushPromises();
    await choose(wrapper, '#linear-connection', 'linear');
    await choose(wrapper, '#linear-team', 'Engineering');
    await searchIssues(wrapper);
    await clickText(wrapper, 'ENG-1');
    expect(navigations.at(-1)).toEqual({ path: 'issues/namespaces/default/detail/linear/issue-1', replace: false });

    await wrapper.get('a.k-back-action').trigger('click');
    await flushPromises();
    await clickText(wrapper, 'Create issue');
    expect(navigations.at(-1)).toEqual({ path: 'issues/namespaces/default/create', replace: false });
  });

  it('refreshes the cached scope after creating a connection and browsing from its detail', async () => {
    const f = fixture([]);
    const wrapper = await render({ ...f.ctx, subPath: 'issues/namespaces/default' });

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
    const wrapper = await render({ ...f.ctx, subPath: 'issues/namespaces/default' });
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
    const wrapper = await render({ ...f.ctx, subPath: 'issues/namespaces/default' });
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

  it('restores the default namespace and scope when host history returns to bare connections', async () => {
    const f = fixture();
    const navigations: Navigation[] = [];
    const wrapper = await render({ ...f.ctx, subPath: 'connections' }, navigations);
    expect((wrapper.get('#linear-namespace').element as HTMLInputElement).value).toBe('default');

    await clickText(wrapper, 'Issues');
    expect(navigations.at(-1)).toEqual({ path: 'issues/namespaces/default', replace: false });
    await wrapper.get('#linear-namespace').setValue('team-a');
    await wrapper.get('form.linear-namespace').trigger('submit');
    await flushPromises();
    expect(navigations.at(-1)).toEqual({ path: 'issues/namespaces/team-a', replace: true });

    await wrapper.setProps({ ctx: { ...f.ctx, subPath: 'connections' } });
    await flushPromises();
    const connectionReads = f.calls.filter(call => call.path.includes('/connections?'));
    expect({
      namespace: (wrapper.get('#linear-namespace').element as HTMLInputElement).value,
      path: connectionReads.at(-1)?.path,
    }).toMatchObject({ namespace: 'default', path: expect.stringContaining('/namespaces/default/') });
  });

  it('keeps the last issue page visible when activation refresh fails', async () => {
    const f = fixture();
    const wrapper = await render({ ...f.ctx, subPath: 'issues/namespaces/default' });
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
    const wrapper = await render({ ...f.ctx, subPath: 'issues/namespaces/default/detail/linear/issue-1' });
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
