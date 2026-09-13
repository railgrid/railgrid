import type { API, FarosContext } from './api';

/** Read-only subset of Factory's published Task contract. No provider implementation imports. */
export type FactoryTask = {
  metadata: { name: string; uid?: string; namespace?: string; deletionTimestamp?: string };
  spec?: { repository?: string; execution?: { approvedInput?: { authorization?: {
    kind?: string; connection?: string; connectionUID?: string; teamID?: string; issueID?: string;
  } } } };
  status?: {
    phase?: string; blocker?: string; attemptCount?: number;
    linear?: { connection?: string; connectionUID?: string; teamID?: string; issueUUID?: string; issueVerified?: boolean };
    publication?: { phase?: string; pullRequest?: string };
    delivery?: { phase?: string; merged?: boolean; pullRequest?: string };
    clarification?: { phase?: string; questionText?: string };
  };
};
export type FactorySnapshot = { enabled: boolean; tasks: FactoryTask[] };
export type FactoryScope = { connection: string; team?: string; issue?: string };
export function workspaceHref(context: FarosContext, path: string) {
  return `/ui/${encodeURIComponent(context.orgUUID || '')}/${encodeURIComponent(context.workspaceUUID || '')}/${path}`;
}
export const factoryHref = (context: FarosContext, task?: FactoryTask) => workspaceHref(context, `providers/factory/tasks${task ? '/' + encodeURIComponent(task.metadata.name) : ''}`);

export function linkedIssue(task: FactoryTask, connection: string, uid: string, team?: string): string | undefined {
  if (!uid || task.metadata.deletionTimestamp || (task.metadata.namespace && task.metadata.namespace !== 'default')) return;
  const auth = task.spec?.execution?.approvedInput?.authorization;
  if (auth) {
    if (auth.kind === 'linear-issue' && auth.connection === connection && auth.connectionUID === uid && (!team || auth.teamID === team)) return auth.issueID;
    return; // Never fall back from a conflicting immutable source to a projection.
  }
  const link = task.status?.linear;
  if (link?.issueVerified && link.connection === connection && link.connectionUID === uid && (!team || link.teamID === team)) return link.issueUUID;
}
export async function readFactory(api: API, context: FarosContext, scope: FactoryScope): Promise<FactorySnapshot> {
  const base = `/api/orgs/${encodeURIComponent(context.orgUUID || '')}/workspaces/${encodeURIComponent(context.workspaceUUID || '')}/providers/enabled`;
  const enabled = await api.request(base);
  if (!enabled || (!enabled.bindingNamesByProvider && !enabled.bindingsByProvider)) throw new Error('Provider availability could not be confirmed.');
  const binding = enabled.bindingsByProvider?.factory;
  if (binding?.terminating) throw new Error('Factory is being disabled in this workspace.');
  if (!binding && !enabled.bindingNamesByProvider?.factory) return { enabled: false, tasks: [] };
  const connection = await api.get('connections', scope.connection);
  if (!connection.metadata.uid || connection.metadata.deletionTimestamp) throw new Error('Refresh the Linear connection before loading linked work.');
  const tasks: FactoryTask[] = [];
  const seen = new Set<string>(); let cursor = '';
  const path = `/clusters/${encodeURIComponent(context.tenant || '')}/apis/factory.providers.faros.sh/v1alpha1/namespaces/default/tasks`;
  do {
    const page = await api.request(path + '?limit=100' + (cursor ? '&continue=' + encodeURIComponent(cursor) : ''));
    if (!Array.isArray(page?.items)) throw new Error('Factory returned an invalid task list.');
    for (const task of page.items as FactoryTask[]) {
      if (!task.metadata?.name) continue;
      const issue = linkedIssue(task, scope.connection, connection.metadata.uid, scope.team);
      if (issue && (!scope.issue || scope.issue === issue)) tasks.push(task);
    }
    cursor = page.metadata?.continue || '';
    if (cursor && (seen.has(cursor) || seen.size >= 1000)) throw new Error('Factory pagination did not finish. Retry loading linked work.');
    seen.add(cursor);
  } while (cursor);
  return { enabled: true, tasks: [...new Map(tasks.map(task => [task.metadata.uid || task.metadata.name, task])).values()] };
}
export function factoryProgress(task: FactoryTask): { label: string; tone: 'muted' | 'warning' | 'success' | 'danger' } {
  const s = task.status;
  if (s?.phase === 'Completed' && s.delivery?.phase === 'Merged' && s.delivery.merged) return { label: 'Delivered', tone: 'success' };
  if (s?.clarification?.phase === 'AwaitingAnswer' || ['NeedsInput', 'needs_input'].includes(s?.phase || '')) return { label: 'Needs input', tone: 'warning' };
  if (s?.blocker || ['Blocked', 'Conflict'].includes(s?.clarification?.phase || '')) return { label: 'Blocked', tone: 'warning' };
  const labels: Record<string, { label: string; tone: 'muted' | 'warning' | 'success' | 'danger' }> = {
    Queued: { label: 'Queued', tone: 'muted' }, Assigned: { label: 'Assigned', tone: 'muted' }, Running: { label: 'Running', tone: 'muted' },
    WaitingForWorker: { label: 'Waiting for worker', tone: 'muted' }, Resuming: { label: 'Resuming', tone: 'muted' },
    AwaitingReview: { label: 'Awaiting review', tone: 'muted' }, Published: { label: 'Awaiting review', tone: 'muted' },
    Paused: { label: 'Paused', tone: 'muted' }, Blocked: { label: 'Blocked', tone: 'warning' }, Failed: { label: 'Failed', tone: 'danger' },
    Cancelled: { label: 'Cancelled', tone: 'muted' }, Completed: { label: 'Delivery unconfirmed', tone: 'warning' },
  };
  return labels[s?.phase || ''] || { label: s?.phase || 'Pending', tone: 'muted' };
}
export function pullRequestURL(task: FactoryTask): string | undefined {
  try { const url = new URL(task.status?.publication?.pullRequest || ''); if (url.protocol === 'https:' && url.hostname === 'github.com' && !url.username && !url.password && /^\/[^/]+\/[^/]+\/pull\/\d+$/.test(url.pathname)) return url.href; } catch {}
}
