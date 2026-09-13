import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h, reactive } from 'vue';
import { API, RequestError, type FarosContext } from './api';
import { factoryHref, factoryProgress, linkedIssue, pullRequestURL, readFactory, type FactoryTask } from './factory';
import { useFactory } from './useFactory';
import { sessionKey, type Session } from './state';
import FactoryIntegration from './components/FactoryIntegration.vue';
const context = { tenant: 'cluster-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a' };
function task(overrides: Partial<FactoryTask> = {}): FactoryTask { return { metadata: { name: 'task-a', uid: 'task-uid', namespace: 'default' }, spec: { execution: { approvedInput: { authorization: { kind: 'linear-issue', connection: 'main', connectionUID: 'connection-uid', teamID: 'team-a', issueID: 'issue-a' } } } }, status: { phase: 'Running' }, ...overrides }; }
function api(){ return new API(context, new AbortController().signal); }
const wrappers: ReturnType<typeof mount>[] = [];
afterEach(() => { wrappers.splice(0).forEach(w=>w.unmount()); vi.restoreAllMocks(); sessionStorage.clear(); });
function transport(enabled: unknown = { bindingNamesByProvider: { factory: 'factory-binding' } }) {
 return vi.spyOn(API.prototype, 'request').mockImplementation(async path => {
  if (path.endsWith('/providers/enabled')) return enabled;
  if (path.endsWith('/connections/main')) return { metadata: { name: 'main', uid: 'connection-uid' } };
  if (path.includes('/tasks?')) return { items: [task()] };
  throw new Error(`Unexpected path ${path}`);
 });
}
function render(ctx: FarosContext = context) {
 const current = reactive({ ...ctx });
 const session: Session = { context: () => current, signal: new AbortController().signal, writes: {}, selection: { connection: 'main', team: 'team-a', query: '' }, draft: { title:'',description:'',stateID:'',returnToIssue:false }, navigate:vi.fn(),href:p=>p };
 let integration!: ReturnType<typeof useFactory>;
 const host = defineComponent({ setup(){ integration = useFactory(()=>({ connection:'main',team:'team-a' }));return()=>h(FactoryIntegration,{integration}); } });
 const wrapper=mount(host,{global:{provide:{[sessionKey as symbol]:session}}});wrappers.push(wrapper);return {wrapper,current,get integration(){return integration;}};
}
describe('workspace-scoped Factory integration',()=>{
 it('checks workspace enablement before any Factory or connection read',async()=>{
  const request=transport({bindingNamesByProvider:{}});
  expect(await readFactory(api(),context,{connection:'main'})).toEqual({enabled:false,tasks:[]});
  expect(request).toHaveBeenCalledTimes(1);expect(request.mock.calls[0][0]).toBe('/api/orgs/org-a/workspaces/workspace-a/providers/enabled');
 });
 it('does not turn discovery failures, malformed data or a terminating binding into disabled',async()=>{
  const request=transport(null);await expect(readFactory(api(),context,{connection:'main'})).rejects.toThrow('confirmed');
  request.mockRejectedValueOnce(new RequestError(403));await expect(readFactory(api(),context,{connection:'main'})).rejects.toThrow('403');
  request.mockResolvedValueOnce({bindingsByProvider:{factory:{terminating:true}}});await expect(readFactory(api(),context,{connection:'main'})).rejects.toThrow('being disabled');
 });
 it('joins immutable identity, rejects replaced connections and never matches issue identifiers',()=>{
  expect(linkedIssue(task(),'main','connection-uid','team-a')).toBe('issue-a');
  for(const [connection,uid,team] of [['main','replacement','team-a'],['other','connection-uid','team-a'],['main','connection-uid','team-b']])expect(linkedIssue(task(),connection,uid,team)).toBeUndefined();
  expect(linkedIssue(task({metadata:{name:'deleted',deletionTimestamp:'now'}}),'main','connection-uid')).toBeUndefined();
  const conflict=task();conflict.spec!.execution!.approvedInput!.authorization!.connectionUID='old';conflict.status={linear:{issueVerified:true,connection:'main',connectionUID:'connection-uid',issueUUID:'issue-a'}};
  expect(linkedIssue(conflict,'main','connection-uid')).toBeUndefined();
 });
 it('reads every page once, filters team identity, and rejects repeated cursors',async()=>{
  const request=transport();const base=request.getMockImplementation()!;
  request.mockImplementation(async(path,...args)=>path.includes('/tasks?')?path.includes('continue=')?{items:[task({metadata:{name:'task-b',uid:'b'}})],metadata:{}}:{items:[task()],metadata:{continue:'next'}}:base(path,...args));
  const value=await readFactory(api(),context,{connection:'main',team:'team-a'});expect(value.tasks).toHaveLength(2);
  expect(request.mock.calls.filter(([p])=>p.includes('/tasks?')).every(([p])=>p.startsWith('/clusters/cluster-a/apis/factory.providers.faros.sh/v1alpha1/namespaces/default/tasks'))).toBe(true);
  request.mockImplementation(async(path,...args)=>path.includes('/tasks?')?{items:[],metadata:{continue:'loop'}}:base(path,...args));
  await expect(readFactory(api(),context,{connection:'main'})).rejects.toThrow('pagination');
 });
 it('keeps completion distinct from delivery and shows clarification before ordinary execution',()=>{
  expect(factoryProgress(task({status:{phase:'Completed'}})).label).toBe('Delivery unconfirmed');
  expect(factoryProgress(task({status:{phase:'Completed',delivery:{phase:'Merged',merged:true}}})).label).toBe('Delivered');
  expect(factoryProgress(task({status:{phase:'Running',clarification:{phase:'AwaitingAnswer'}}})).label).toBe('Needs input');
  expect(factoryProgress(task({status:{phase:'NewPhase'}})).label).toBe('NewPhase');
  expect(factoryHref(context,task())).toBe('/ui/org-a/workspace-a/providers/factory/tasks/task-a');
  expect(pullRequestURL(task({status:{publication:{pullRequest:'javascript:alert(1)'}}}))).toBeUndefined();
 });
 it('shows loading, persists dismissal only in its workspace, and clears data on workspace changes',async()=>{
  transport({bindingNamesByProvider:{}});const {wrapper,current}=render();
  expect(wrapper.text()).toContain('Checking Factory');await flushPromises();expect(wrapper.text()).toContain('not enabled');
  await wrapper.findAll('button').find(b=>b.text()==='Not now')!.trigger('click');expect(wrapper.text()).not.toContain('Explore Factory');
  current.workspaceUUID='workspace-b';await flushPromises();expect(wrapper.text()).toContain('Explore Factory');expect(wrapper.find('a').attributes('href')).toBe('/ui/org-a/workspace-b/providers');
 });
 it('hides linked tasks after a permission failure without suggesting enablement',async()=>{
  const request=transport();const view=render();await flushPromises();expect(view.wrapper.text()).toContain('1 linked issue');
  request.mockRejectedValueOnce(new RequestError(403));view.integration.load();await flushPromises();
  expect(view.wrapper.text()).toContain('status is unavailable');expect(view.wrapper.text()).not.toContain('Explore Factory');expect(view.integration.tasks.value).toEqual([]);
 });
 it('does not infer missing configuration from an empty task list',async()=>{
  const request=transport();const base=request.getMockImplementation()!;request.mockImplementation(async(p,...args)=>p.includes('/tasks?')?{items:[]}:base(p,...args));
  const view=render();await flushPromises();expect(view.wrapper.text()).toContain('No linked work yet');expect(view.wrapper.text()).not.toContain('setup needed');
 });
 it('ignores a late response after a scope change',async()=>{
  let finish!:(v:unknown)=>void;const request=transport();
  request.mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;}));const view=render();view.current.workspaceUUID='workspace-b';await flushPromises();
  expect(view.wrapper.text()).toContain('1 linked issue');finish({bindingNamesByProvider:{}});await flushPromises();expect(view.wrapper.text()).toContain('1 linked issue');expect(view.wrapper.text()).not.toContain('Explore Factory');
 });
});
