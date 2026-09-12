import { providerFetch, type ProviderFetchContext } from './portalkit/tenant';

export type FarosContext = ProviderFetchContext & {
  tenant?: string; theme?: string; subPath?: string; basePath?: string;
  orgUUID?: string; workspaceUUID?: string; user?: { sub?: string; email?: string };
};
export type Node = { id: string; name?: string; key?: string; identifier?: string; title?: string; description?: string; body?: string; url?: string; updatedAt?: string; team?: Node; state?: Node };
export type Result = Partial<Node> & { nodes?: Node[]; pageInfo?: { hasNextPage: boolean; endCursor: string } };
export type Resource = {
  metadata: { name: string; namespace?: string; creationTimestamp?: string };
  spec?: Record<string, unknown>;
  status?: { ready?: boolean; phase?: string; message?: string; checkedAt?: string; startedAt?: string; completedAt?: string; result?: Result };
};
export class OperationError extends Error {
  constructor(public operationName: string, message: string) { super(`${operationName}: ${message}`); }
}
export class API {
  constructor(private context: FarosContext, private namespace: string, private signal: AbortSignal) {}
  async request(path: string, body?: unknown) {
    this.signal.throwIfAborted();
    const response = await providerFetch(this.context)(path, { method: body ? 'POST' : 'GET', signal: this.signal, headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
    this.signal.throwIfAborted();
    if (!response.ok) throw new Error(`Request failed (${response.status}). Check workspace access and provider readiness.`);
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
  createConnection(name: string, secret: string, teams: string[]): Promise<Resource> {
    return this.request(this.path('connections'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', metadata: { name, namespace: this.namespace }, spec: { apiKeySecretRef: { name: secret, key: 'apiKey' }, teams: teams.map(id => ({ id })) } });
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
