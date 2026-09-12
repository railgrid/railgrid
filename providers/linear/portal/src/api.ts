import { providerFetch, type ProviderFetchContext } from './portalkit/tenant';

export type FarosContext = ProviderFetchContext & {
  tenant?: string; theme?: string; subPath?: string; basePath?: string;
  orgUUID?: string; workspaceUUID?: string; user?: { sub?: string; email?: string };
};
export type Node = { id: string; name?: string; key?: string; identifier?: string; title?: string; description?: string; body?: string; createdAt?: string; editedAt?: string; parentId?: string; user?: { name?: string; displayName?: string }; botActor?: { name?: string; type?: string }; externalUser?: { id: string }; url?: string; updatedAt?: string; team?: Node; state?: Node };
export type Result = Partial<Node> & { nodes?: Node[]; pageInfo?: { hasNextPage: boolean; endCursor: string } };
export type Resource = {
  metadata: { name: string; namespace?: string; resourceVersion?: string; creationTimestamp?: string };
  spec?: Record<string, unknown>;
  status?: { ready?: boolean; phase?: string; message?: string; checkedAt?: string; startedAt?: string; completedAt?: string; result?: Result };
};
export class RequestError extends Error {
  constructor(public status: number) {
    const guidance: Record<number, string> = { 400: 'Check the submitted fields.', 401: 'Sign in again, then retry.', 403: 'Ask your workspace administrator for access.', 404: 'The resource was not found. Refresh the collection.', 409: 'The resource already exists or has changed. Inspect it before retrying.', 410: 'This page expired. Refresh to start from the first page.', 422: 'Check the names and team UUIDs.', 429: 'Too many requests. Wait before retrying.' };
    super(`Request failed (${status}). ${guidance[status] || 'The provider is unavailable. Retry when it is ready.'}`);
  }
}
export class ConnectionError extends Error {
  constructor(public connectionName: string, message: string) { super(message); }
}
export class OperationError extends Error {
  constructor(public operationName: string, message: string) { super(`${operationName}: ${message}`); }
}
export class API {
  constructor(private context: FarosContext, private namespace: string, private signal: AbortSignal) {}
  async request(path: string, body?: unknown, method = body ? 'POST' : 'GET') {
    this.signal.throwIfAborted();
    const response = await providerFetch(this.context)(path, { method, signal: this.signal, headers: body ? { 'Content-Type': method === 'PATCH' ? 'application/merge-patch+json' : 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
    this.signal.throwIfAborted();
    if (!response.ok) throw new RequestError(response.status);
    const value = await response.json();
    this.signal.throwIfAborted();
    return value;
  }
  path(resource: string, name = '') {
    return `/clusters/${encodeURIComponent(this.context.tenant || '')}/apis/linear.providers.faros.sh/v1alpha1/namespaces/${encodeURIComponent(this.namespace)}/${resource}${name ? '/' + encodeURIComponent(name) : ''}`;
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
  updateTeams(name: string, teams: string[], resourceVersion: string): Promise<Resource> {
    return this.request(this.path('connections', name), { metadata: { resourceVersion }, spec: { teams: teams.map(id => ({ id })) } }, 'PATCH');
  }
  async createConnection(name: string, secret: string, teams: string[]): Promise<Resource> {
    const spec = { apiKeySecretRef: { name: secret, key: 'apiKey' }, teams: [...new Set(teams)].map(id => ({ id })) };
    try {
      return await this.request(this.path('connections'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', metadata: { name, namespace: this.namespace }, spec });
    } catch (error) {
      this.signal.throwIfAborted();
      if (error instanceof RequestError && error.status < 500 && error.status !== 409) throw error;
      let existing: Resource;
      try { existing = await this.get('connections', name); }
      catch { this.signal.throwIfAborted(); throw new ConnectionError(name, 'Creation outcome unknown. Inspect this connection before submitting again.'); }
      const ref = existing.spec?.apiKeySecretRef as { name?: string; key?: string } | undefined;
      const ids = (existing.spec?.teams as { id: string }[] | undefined || []).map(t => t.id).sort();
      if (ref?.name === secret && (ref.key || 'apiKey') === 'apiKey' && JSON.stringify(ids) === JSON.stringify(spec.teams.map(t => t.id).sort())) return existing;
      throw new ConnectionError(name, 'A connection with this name exists with different settings. Inspect it or choose another name.');
    }
  }
  async discover(connection: string, action: 'teams' | 'states', teamID = ''): Promise<Node[]> {
    const nodes: Node[] = [];
    let after = '';
    do {
      const page = await this.operation(connection, { action, teamID, first: 50, ...(after ? { after } : {}) });
      nodes.push(...(page.nodes || []));
      if (!page.pageInfo?.hasNextPage) break;
      if (!page.pageInfo.endCursor || page.pageInfo.endCursor === after) throw new Error('Discovery pagination did not advance. Retry discovery.');
      after = page.pageInfo.endCursor;
    } while (true);
    return nodes;
  }
  async operation(connection: string, fields: Record<string, unknown>): Promise<Result> {
    const name = 'op-' + crypto.randomUUID();
    try {
      await this.request(this.path('operations'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Operation', metadata: { name, namespace: this.namespace }, spec: { connection, ...fields } });
    } catch (error) {
      this.signal.throwIfAborted();
      throw new OperationError(name, `submission outcome unknown. Inspect Operations before repeating it. ${error instanceof Error ? error.message : ''}`);
    }
    for (let i = 0; i < 30; i++) {
      let op: Resource;
      try { op = await this.get('operations', name); }
      catch (error) {
        this.signal.throwIfAborted();
        throw new OperationError(name, `status could not be read. Inspect Operations before repeating it. ${error instanceof Error ? error.message : ''}`);
      }
      if (op.status?.phase === 'Succeeded') return op.status.result || {};
      if (op.status?.phase === 'Failed' || op.status?.phase === 'Uncertain') throw new OperationError(name, `${op.status.message || op.status.phase}. Inspect this operation before submitting another write.`);
      await new Promise<void>((resolve, reject) => {
        this.signal.throwIfAborted();
        const abort = () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); };
        const timer = setTimeout(() => { this.signal.removeEventListener('abort', abort); resolve(); }, 1000);
        this.signal.addEventListener('abort', abort, { once: true });
      });
    }
    throw new OperationError(name, 'is still pending. Refresh Operations to inspect it before repeating a write.');
  }
}
