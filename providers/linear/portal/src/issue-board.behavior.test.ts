import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h, KeepAlive, reactive, ref } from 'vue';
import { API } from './api';
import { sessionKey, type Session } from './state';
import IssueBoard from './components/IssueBoard.vue';
import RegisteredTeams from './components/RegisteredTeams.vue';
import IssuesView from './views/IssuesView.vue';
const wrappers: ReturnType<typeof mount>[] = [];
function session(): Session { return { context: () => ({ tenant: 'workspace' }), signal: new AbortController().signal, writes: {}, draft: { title: '', description: '', stateID: '', returnToIssue: false }, selection: reactive({ connection: 'main', team: 'team', query: '' }), navigate: vi.fn(), href: path => `/ui/${path}` }; }
function render(component: any, props: any, state = session()) { const wrapper = mount(component, { props, global: { provide: { [sessionKey as symbol]: state } } }); wrappers.push(wrapper); return wrapper; }
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks(); localStorage.clear(); });
function workflow() { vi.spyOn(API.prototype, 'discover').mockResolvedValue([{ id: 'todo', name: 'Todo' }, { id: 'done', name: 'Done' }]); }
describe('Linear issue presentation', () => {
  it('restarts an interrupted first board load when returning to the cached view', async () => {
    let releaseOld!: (value: import('./api').Node[]) => void;
    vi.spyOn(API.prototype, 'discover').mockImplementationOnce(() => new Promise(resolve => { releaseOld = resolve; })).mockResolvedValue([{ id: 'todo', name: 'Todo' }]);
    vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [{ id: 'a', title: 'Current issue', state: { id: 'todo' } }] });
    const visible = ref(true);
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => visible.value ? h(IssueBoard, { connection: 'main', team: 'team', query: '', revision: 0 }) : null }) });
    const wrapper = render(host, {}); await flushPromises();
    expect(wrapper.find('[data-testid="issue-board-loading"]').exists()).toBe(true);
    visible.value = false; await flushPromises(); visible.value = true; await flushPromises();
    expect(wrapper.get('a.linear-issue-card-link').text()).toContain('Current issue');
    releaseOld([{ id: 'old', name: 'Old state' }]); await flushPromises();
    expect(wrapper.text()).not.toContain('Old state');
  });
  it('retains the board query after visiting List and returning to the cached view', async () => {
    workflow();
    const operation = vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [] });
    const visible = ref(true);
    const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => visible.value ? h(IssuesView, { scope: { connection: 'main', team: 'team' } }) : null }) });
    const wrapper = render(host, {}); await flushPromises();
    async function chooseLayout(current: string, next: string) {
      await wrapper.get(`[aria-label="Issue layout: ${current}"]`).trigger('click'); await flushPromises();
      [...document.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')].find(el => el.textContent?.trim() === next)!.click(); await flushPromises();
    }
    await chooseLayout('Board', 'List'); await chooseLayout('List', 'Board');
    await wrapper.get('#linear-query').setValue('board query'); await wrapper.get('form.linear-fields').trigger('submit'); await flushPromises();
    await wrapper.get('#linear-query').setValue('');
    expect(wrapper.text()).toContain('Clear search'); expect(wrapper.text()).toContain('results for “board query”');
    await wrapper.get('[aria-label="Issue layout: Board"]').trigger('click'); await flushPromises();
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
    visible.value = false; await flushPromises();
    expect(document.querySelector('[role="menu"]')).toBeNull();
    visible.value = true; await flushPromises();
    expect(operation.mock.calls.filter(([, fields]) => fields.action === 'issues').at(-1)?.[1].query).toBe('board query');
    await wrapper.findAll('button').find(button => button.text() === 'Clear search')!.trigger('click'); await flushPromises();
    expect(operation.mock.calls.at(-1)?.[1].query).toBe('');
  });
  it('retains registered teams on refresh failure but clears them on connection replacement', async () => {
    const list = vi.spyOn(API.prototype, 'list').mockResolvedValueOnce({ items: [{ metadata: { name: 'team' }, spec: { connection: 'main', connectionUID: 'one' }, status: { name: 'Registered team' } }] });
    const wrapper = render(RegisteredTeams, { connection: { metadata: { name: 'main', uid: 'one' } } }); await flushPromises();
    let rejectRefresh!: (error: Error) => void;
    list.mockImplementationOnce(() => new Promise((_, reject) => { rejectRefresh = reject; }));
    await wrapper.setProps({ connection: { metadata: { name: 'main', uid: 'one' }, status: { ready: true } } });
    rejectRefresh(new Error('Refresh failed')); await flushPromises();
    expect(wrapper.text()).toContain('Registered team'); expect(wrapper.text()).toContain('Refresh failed');
    list.mockResolvedValueOnce({ items: [] });
    await wrapper.setProps({ connection: { metadata: { name: 'main', uid: 'two' } } }); await flushPromises();
    expect(wrapper.text()).not.toContain('Registered team');
  });

  it('shows a skeleton through discovery and issue loading, then retains cards during refresh and failure', async () => {
    let finishDiscovery!: (value: import('./api').Node[]) => void;
    let finishIssues!: (value: import('./api').Result) => void;
    const discover = vi.spyOn(API.prototype, 'discover').mockImplementationOnce(() => new Promise(resolve => { finishDiscovery = resolve; }));
    vi.spyOn(API.prototype, 'action').mockImplementationOnce(() => new Promise(resolve => { finishIssues = resolve; }));
    const wrapper = render(IssueBoard, { connection: 'main', team: 'team', query: '', revision: 0 });
    expect(wrapper.attributes('aria-busy')).toBe('true');
    expect(wrapper.get('[data-testid="issue-board-loading"]').attributes('aria-hidden')).toBe('true');
    expect(wrapper.findAll('[data-testid="issue-board-loading"] .linear-issue-lane')).toHaveLength(4);
    expect(wrapper.text()).not.toContain('No matching issues');
    finishDiscovery([{ id: 'todo', name: 'Todo' }]); await flushPromises();
    expect(wrapper.find('[data-testid="issue-board-loading"]').exists()).toBe(true);
    finishIssues({ nodes: [{ id: 'a', title: 'Visible issue', state: { id: 'todo', name: 'Todo' } }] }); await flushPromises();
    expect(wrapper.find('[data-testid="issue-board-loading"]').exists()).toBe(false);
    expect(wrapper.attributes('aria-busy')).toBe('false');
    let failRefresh!: (reason: Error) => void;
    discover.mockImplementationOnce(() => new Promise((_, reject) => { failRefresh = reject; }));
    await wrapper.setProps({ revision: 1 });
    expect(wrapper.text()).toContain('Refreshing issue board');
    expect(wrapper.get('a.linear-issue-card-link').text()).toContain('Visible issue');
    expect(wrapper.find('[data-testid="issue-board-loading"]').exists()).toBe(false);
    failRefresh(new Error('Connection unavailable')); await flushPromises();
    expect(wrapper.text()).toContain('Connection unavailable');
    expect(wrapper.get('a.linear-issue-card-link').text()).toContain('Visible issue');
    expect(wrapper.text()).toContain('Retry board');
  });
  it('uses workflow categories for icons in headings and cards, including renamed statuses', async () => {
    vi.spyOn(API.prototype, 'discover').mockResolvedValue([{ id: 'review', name: 'Ready for QA', type: 'started' }, { id: 'shipped', name: 'Released', type: 'completed' }, { id: 'custom', name: 'Custom', type: 'future' }]);
    vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [{ id: 'a', title: 'Check release', state: { id: 'review' } }, { id: 'b', title: 'Shipped change', state: { id: 'shipped' } }, { id: 'c', title: 'Unknown category', state: { id: 'custom' } }] });
    const wrapper = render(IssueBoard, { connection: 'main', team: 'team', query: '', revision: 0 }); await flushPromises();
    const lanes = wrapper.findAll('.linear-issue-lane');
    for (const [index, tone] of ['active', 'complete', 'neutral'].entries()) {
      expect(lanes[index].findAll(`.linear-workflow-icon--${tone}`)).toHaveLength(2);
      expect(lanes[index].get('a .linear-workflow-icon').attributes('aria-hidden')).toBe('true');
    }
    expect(lanes[0].get('a').text()).toContain('Ready for QA:');
  });
  it('loads all cursor pages before counting lanes and links cards to Faros', async () => {
    workflow(); const state = session();
    const operation = vi.spyOn(API.prototype, 'action').mockResolvedValueOnce({ nodes: [{ id: 'a', identifier: 'FAR-1', title: 'First', state: { id: 'todo', name: 'Todo' } }], pageInfo: { hasNextPage: true, endCursor: 'next' } }).mockResolvedValueOnce({ nodes: [{ id: 'b', title: 'Second', state: { id: 'done', name: 'Done' } }], pageInfo: { hasNextPage: false, endCursor: '' } });
    const wrapper = render(IssueBoard, { connection: 'main', team: 'team', query: 'fix', revision: 0 }, state); await flushPromises();
    expect(operation.mock.calls[1][1]).toMatchObject({ after: 'next', query: 'fix', teamID: 'team' });
    expect(wrapper.findAll('.linear-issue-card')).toHaveLength(2);
    expect(wrapper.findAll('.linear-issue-lane-heading').map(x => x.text())).toEqual(['Todo1', 'Done1']);
    const card = wrapper.get('a.linear-issue-card-link');
    const originalHref = card.attributes('href');
    card.element.setAttribute('href', '#modified-click');
    for (const modifier of ['ctrlKey', 'metaKey', 'shiftKey']) {
      const event = new MouseEvent('click', { bubbles: true, cancelable: true, [modifier]: true });
      card.element.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(false); expect(state.navigate).not.toHaveBeenCalled();
    }
    card.element.setAttribute('href', originalHref!);
    await card.trigger('click'); expect(state.navigate).toHaveBeenCalledWith('issues/detail/main/a/team');
  });
  it('opens Linear externally while keeping the Factory task inside Faros and inside the card', async () => {
    workflow(); const state = session();
    const url = 'https://linear.app/faros-sh/issue/FAR-1/example';
    vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [{ id: 'a', identifier: 'FAR-1', title: 'Example', url, state: { id: 'todo' } }] });
    const wrapper = render(IssueBoard, { connection: 'main', team: 'team', query: '', revision: 0, factoryTasks: { a: [{ metadata: { name: 'factory-task' } }] } }, state); await flushPromises();
    const card = wrapper.get('.linear-issue-card');
    const issue = card.get('a.linear-issue-card-link');
    expect(issue.attributes('href')).toBe(url);
    expect(issue.attributes('target')).toBe('_blank');
    expect(issue.attributes('rel')).toBe('noopener noreferrer');
    const factory = card.get('a.linear-factory-link');
    expect(factory.attributes('href')).toContain('/providers/factory/');
    expect(factory.attributes('target')).toBeUndefined();
    expect(card.find('a a').exists()).toBe(false);
    expect(state.navigate).not.toHaveBeenCalled();
  });
  it('reports a repeated cursor without rendering an incomplete board as complete', async () => {
    workflow(); vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [], pageInfo: { hasNextPage: true, endCursor: 'same' } });
    const wrapper = render(IssueBoard, { connection: 'main', team: 'team', query: '', revision: 0 }); await flushPromises();
    expect(wrapper.text()).toContain('pagination did not advance'); expect(wrapper.find('.linear-issue-board').exists()).toBe(false);
  });
  it('defaults scoped issues to Board and remembers the table preference', async () => {
    workflow(); vi.spyOn(API.prototype, 'action').mockResolvedValue({ nodes: [], pageInfo: { hasNextPage: false, endCursor: '' } });
    const wrapper = render(IssuesView, { scope: { connection: 'main', team: 'team' } }); await flushPromises();
    expect(wrapper.findComponent(IssueBoard).exists()).toBe(true);
    await wrapper.get('[aria-label="Issue layout: Board"]').trigger('click'); await flushPromises();
    const options = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')];
    expect(options.map(option => option.textContent?.trim())).toEqual(['Board', 'List']);
    expect(options[0].getAttribute('aria-checked')).toBe('true');
    options[1].click(); await flushPromises();
    expect(wrapper.get('[aria-label="Issue layout: List"]').attributes('aria-expanded')).toBe('false');
    expect(wrapper.findComponent(IssueBoard).exists()).toBe(false); expect(localStorage.getItem('faros:linear:issue-view')).toBe('table');
    expect(render(IssuesView, { scope: { connection: 'main', team: 'team' } }).findComponent(IssueBoard).exists()).toBe(false);
  });
  it('lists only registrations owned by the current connection identity', async () => {
    vi.spyOn(API.prototype, 'list').mockResolvedValue({ items: [
      { metadata: { name: 'current' }, spec: { connection: 'main', connectionUID: 'new' }, status: { name: 'Current team', ready: true } },
      { metadata: { name: 'old' }, spec: { connection: 'main', connectionUID: 'old' }, status: { name: 'Old team' } },
      { metadata: { name: 'other' }, spec: { connection: 'other', connectionUID: 'new' }, status: { name: 'Other team' } },
    ] });
    const wrapper = render(RegisteredTeams, { connection: { metadata: { name: 'main', uid: 'new' } } }); await flushPromises();
    expect(wrapper.text()).toContain('Current team'); expect(wrapper.text()).not.toContain('Old team'); expect(wrapper.text()).not.toContain('Other team');
    expect(wrapper.get('a').attributes('href')).toContain('teams/detail/current');
  });
});
