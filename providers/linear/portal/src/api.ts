import { providerFetch, type ProviderFetchContext } from './portalkit/tenant';

export type FarosContext = ProviderFetchContext & {
  tenant?: string; theme?: string; subPath?: string; basePath?: string;
  orgUUID?: string; workspaceUUID?: string; user?: { sub?: string; email?: string };
};
export type Node = { id: string; type?: string; name?: string; key?: string; identifier?: string; title?: string; description?: string; body?: string; createdAt?: string; editedAt?: string; parentId?: string; user?: { name?: string; displayName?: string }; botActor?: { name?: string; type?: string }; externalUser?: { id: string }; url?: string; updatedAt?: string; team?: Node; state?: Node };
export type Result = Partial<Node> & { nodes?: Node[]; pageInfo?: { hasNextPage: boolean; endCursor: string } };
export type Resource = {
  metadata: { name: string; uid?: string; resourceVersion?: string; creationTimestamp?: string; deletionTimestamp?: string };
  spec?: Record<string, unknown>;
  status?: { ready?: boolean; name?: string; key?: string; phase?: string; message?: string; checkedAt?: string; startedAt?: string; completedAt?: string; result?: Result };
};
export class RequestError extends Error {
  constructor(public status: number) {
    const guidance: Record<number, string> = { 400: 'Check the submitted fields.', 401: 'Sign in again, then retry.', 403: 'Ask your workspace administrator for access.', 404: 'The resource was not found. Refresh the collection.', 409: 'The resource already exists or has changed. Inspect it before retrying.', 410: 'This page expired. Refresh to start from the first page.', 422: 'Check the names and team UUIDs.', 429: 'Too many requests. Wait before retrying.' };
    super(`Request failed (${status}). ${guidance[status] || 'The provider is unavailable. Retry when it is ready.'}`);
  }
}
export class ConnectionError extends Error {
  constructor(public connectionName: string, message: string, public partial = false) { super(message); }
}
export class OperationError extends Error {
  constructor(public operationName: string, message: string) { super(`${operationName}: ${message}`); }
}
export type WriteIntent = { name: string; connection: string; team: string; teamID: string; action: string };
export const writeKey = (action: string, connection: string, issueID = '') => action === 'createIssue' ? 'createIssue' : JSON.stringify([action, connection, issueID]);
export class API {
  private completedWrites = new Set<string>();
  constructor(private context: FarosContext, private signal: AbortSignal, private writes: Record<string, WriteIntent> = {}) {}
  acknowledgeWrites() { for (const name of this.completedWrites) this.forgetWrite(name); }
  private forgetWrite(name: string) { for (const key of Object.keys(this.writes)) if (this.writes[key].name === name) delete this.writes[key]; }
  async request(path: string, body?: unknown, method = body ? 'POST' : 'GET') {
    this.signal.throwIfAborted();
    const response = await providerFetch(this.context)(path, { method, signal: this.signal, headers: body ? { 'Content-Type': method === 'PATCH' ? 'application/merge-patch+json' : 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
    this.signal.throwIfAborted();
    if (!response.ok) throw new RequestError(response.status);
    if (response.status === 204) return undefined;
    const value = await response.json();
    this.signal.throwIfAborted();
    return value;
  }
  path(resource: string, name = '') {
    return `/clusters/${encodeURIComponent(this.context.tenant || '')}/apis/linear.providers.faros.sh/v1alpha1/${resource}${name ? '/' + encodeURIComponent(name) : ''}`;
  }
  get(resource: string, name: string): Promise<Resource> { return this.request(this.path(resource, name)); }
  async list(resource: string): Promise<{ items: Resource[] }> {
    const items: Resource[] = [];
    let cursor = '';
    do {
      const page = await this.request(this.path(resource) + '?limit=100' + (cursor ? '&continue=' + encodeURIComponent(cursor) : ''));
      items.push(...page.items);
      const next = page.metadata?.continue || '';
      if (next && next === cursor) throw new Error('Resource pagination did not advance. Refresh to try again.');
      cursor = next;
    } while (cursor);
    return { items };
  }
  listPage(resource: string, limit = 10, cursor = ''): Promise<{ items: Resource[]; metadata?: { continue?: string } }> {
    return this.request(this.path(resource) + `?limit=${limit}` + (cursor ? '&continue=' + encodeURIComponent(cursor) : ''));
  }
  async deleteConnection(name: string, uid: string): Promise<void> {
    if (!uid) throw new Error('Refresh the connection before deleting it.');
    try {
      await this.request(this.path('connections', name), { apiVersion: 'v1', kind: 'DeleteOptions', preconditions: { uid } }, 'DELETE');
    } catch (error) {
      if (!(error instanceof RequestError && error.status === 404)) throw error;
    }
  }
  async createConnection(name: string, secret: string, secretNamespace = 'default'): Promise<Resource> {
    const spec = { apiKeySecretRef: { name: secret, namespace: secretNamespace, key: 'apiKey' } };
    try {
      return await this.request(this.path('connections'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', metadata: { name }, spec });
    } catch (error) {
      this.signal.throwIfAborted();
      if (error instanceof RequestError && error.status < 500 && error.status !== 409) throw error;
      let existing: Resource;
      try { existing = await this.get('connections', name); }
      catch { this.signal.throwIfAborted(); throw new ConnectionError(name, 'Creation outcome unknown. Inspect this connection before submitting again.', true); }
      const ref = existing.spec?.apiKeySecretRef as { name?: string; key?: string; namespace?: string } | undefined;
      if (ref?.name === secret && (ref.namespace || 'default') === secretNamespace && (ref.key || 'apiKey') === 'apiKey') return existing;
      throw new ConnectionError(name, 'A connection with this name exists with different settings. Inspect it or choose another name.');
    }
  }
  async previewTeams(apiKey: string, after = ''): Promise<Result> {
    try { return await this.request('/services/providers/linear/api/onboarding/teams', { apiKey, after }); }
    catch (error) {
      this.signal.throwIfAborted();
      if (error instanceof RequestError && error.status !== 422) throw error;
      throw new Error('Could not check the API key or retrieve teams. Check the key’s access in Linear and try again.');
    }
  }
  async connectWithKey(name: string, apiKey: string, secret: string): Promise<Resource> {
    const connection = await this.createConnection(name, secret);
    if (!connection.metadata.uid) throw new ConnectionError(name, 'Connection identity is unavailable. Inspect the connection before retrying.', true);
    const path = `/clusters/${encodeURIComponent(this.context.tenant || '')}/api/v1/namespaces/default/secrets`;
    const owner = { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', name, uid: connection.metadata.uid };
    try {
      await this.request(path, { apiVersion: 'v1', kind: 'Secret', metadata: { name: secret, namespace: 'default', ownerReferences: [owner] }, type: 'Opaque', stringData: { apiKey } });
    } catch (error) {
      this.signal.throwIfAborted();
      // Recover a lost response without adopting or overwriting another credential.
      try {
        const existing = await this.request(path + '/' + encodeURIComponent(secret));
        if (existing.metadata?.ownerReferences?.some((ref: typeof owner) => ref.uid === owner.uid && ref.kind === owner.kind && ref.apiVersion === owner.apiVersion) && existing.data?.apiKey === btoa(apiKey)) return connection;
      } catch { this.signal.throwIfAborted(); }
      const guidance = error instanceof RequestError ? error.message : 'The credential save outcome is unknown.';
      throw new ConnectionError(name, `Connection created, but credential storage could not be confirmed. ${guidance} Retry with the same settings or inspect the connection.`, true);
    }
    return connection;
  }
  async discover(connection: string, action: 'teams' | 'states', teamID = ''): Promise<Node[]> {
    const nodes: Node[] = [];
    let after = '';
    do {
      const page = await this.action(connection, { action, teamID, first: 50, ...(after ? { after } : {}) });
      nodes.push(...(page.nodes || []));
      if (!page.pageInfo?.hasNextPage) break;
      if (!page.pageInfo.endCursor || page.pageInfo.endCursor === after) throw new Error('Discovery pagination did not advance. Retry discovery.');
      after = page.pageInfo.endCursor;
    } while (true);
    return nodes;
  }
  availableTeams(connection: string, after = ''): Promise<Result> {
    return this.request(`/services/providers/linear/api/connections/${encodeURIComponent(connection)}/teams${after ? '?after=' + encodeURIComponent(after) : ''}`);
  }
  async addTeam(connection: Resource, teamID: string): Promise<Resource> {
    if (!connection.metadata.uid) throw new Error('Refresh the Connection before adding teams.');
    const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(JSON.stringify([connection.metadata.uid, teamID])));
    const name = 'team-' + Array.from(new Uint8Array(hash)).map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 24);
    const spec = { connection: connection.metadata.name, connectionUID: connection.metadata.uid, teamID };
    try { return await this.request(this.path('teams'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Team', metadata: { name, ownerReferences: [{ apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', name: connection.metadata.name, uid: connection.metadata.uid }] }, spec }); }
    catch (error) {
      this.signal.throwIfAborted();
      if (error instanceof RequestError && error.status < 500 && error.status !== 409) throw error;
      const existing = await this.get('teams', name);
      if (!existing.metadata.deletionTimestamp && existing.spec?.connection === spec.connection && existing.spec?.connectionUID === spec.connectionUID && existing.spec?.teamID === teamID) return existing;
      throw new Error('This team registration conflicts with an existing resource. Refresh Teams before retrying.');
    }
  }
  async removeTeam(name: string, uid: string): Promise<void> {
    if (!uid) throw new Error('Refresh the Team before removing it.');
    try { await this.request(this.path('teams', name), { apiVersion: 'v1', kind: 'DeleteOptions', preconditions: { uid } }, 'DELETE'); }
    catch (error) { if (!(error instanceof RequestError && error.status === 404)) throw error; }
  }
  private actionPath(team: string, action: string) {
    return `/services/providers/linear/actions/clusters/${encodeURIComponent(this.context.tenant || '')}/teams/${encodeURIComponent(team)}/${action}/v1`;
  }
  async action(connection: string, fields: Record<string, unknown>): Promise<Result> {
    this.signal.throwIfAborted();
    const action = String(fields.action);
    const key = ['createIssue', 'updateIssue', 'addComment'].includes(action) ? writeKey(action, connection, String(fields.issueID || '')) : '';
    if (key && this.writes[key]) throw new OperationError(this.writes[key].name, 'A previous write needs inspection. Check its outcome before preparing a separate write.');
    const registrations = (await this.list('teams')).items.filter(t => t.spec?.connection === connection && !t.metadata.deletionTimestamp);
    if (action === 'teams') return { nodes: registrations.map(t => ({ id: String(t.spec?.teamID), name: t.status?.name, key: t.status?.key })) };
    const candidates = registrations.filter(t => !fields.teamID || t.spec?.teamID === fields.teamID);
    if (candidates.length !== 1) throw new Error('Choose a registered Team before using this action.');
    const team = candidates[0].metadata.name;
    const verb = ({ createIssue: 'create_issue', updateIssue: 'update_issue', addComment: 'add_comment' } as Record<string, string>)[action] || action;
    const { action: _action, teamID: _teamID, ...input } = fields;
    const name = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z') + '.' + crypto.randomUUID();
    if (key) this.writes[key] = { name, connection, team, teamID: String(candidates[0].spec?.teamID), action: verb };
    try {
      const response = await this.request(this.actionPath(team, verb), { ...(key ? { requestId: name } : {}), input });
      return this.outcome(name, response.result, !!key);
    } catch (error) {
      this.signal.throwIfAborted();
      if (!key) throw error;
      throw new OperationError(name, `The write outcome needs confirmation. Use Check outcome before repeating it. ${error instanceof Error ? error.message : ''}`);
    }
  }
  private outcome(name: string, outcome: { phase?: string; result?: Result; message?: string }, write: boolean): Result {
    if (outcome.phase === 'Succeeded') { if (write) this.completedWrites.add(name); return outcome.result || {}; }
    if (outcome.phase === 'Failed') this.forgetWrite(name);
    throw new OperationError(name, outcome.message || 'The outcome is not confirmed. Check Linear before starting another write.');
  }
  async inspectWrite(name: string): Promise<Result> {
    const intent = Object.values(this.writes).find(w => w.name === name);
    if (!intent) throw new Error('Write context unavailable. Check Linear before starting another write.');
    const response = await this.request(this.actionPath(intent.team, intent.action) + '?requestId=' + encodeURIComponent(name));
    return this.outcome(name, response.result, true);
  }
}
