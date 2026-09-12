import { providerFetch, type ProviderFetchContext } from './portalkit/tenant';
export type FarosContext = ProviderFetchContext & {
    tenant?: string;
    theme?: string;
};
export type Resource = {
    metadata: {
        name: string;
    };
    spec?: Record<string, unknown>;
    status?: {
        ready?: boolean;
        phase?: string;
        message?: string;
        result?: {
            nodes?: Record<string, unknown>[];
            pageInfo?: {
                hasNextPage: boolean;
                endCursor: string;
            };
            [key: string]: unknown;
        };
    };
};
export class API {
    private fetcher: ReturnType<typeof providerFetch>;
    constructor(private context: FarosContext, private namespace: string, private signal: AbortSignal) { this.fetcher = providerFetch(context); }
    async request(path: string, body?: unknown) {
        const response = await this.fetcher(path, { method: body ? 'POST' : 'GET', signal: this.signal, headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
        if (!response.ok)
            throw new Error(`Request failed (${response.status}). Check workspace access and provider readiness.`);
        const value = await response.json();
        this.signal.throwIfAborted();
        return value;
    }
    path(resource: string, name = '') { return `/clusters/${encodeURIComponent(this.context.tenant || '')}/apis/linear.providers.faros.sh/v1alpha1/namespaces/${encodeURIComponent(this.namespace)}/${resource}${name ? '/' + encodeURIComponent(name) : ''}`; }
    list(resource: string) { return this.request(this.path(resource) + '?limit=100') as Promise<{
        items: Resource[];
    }>; }
    createConnection(name: string, secret: string, teams: string[]) { return this.request(this.path('connections'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Connection', metadata: { name, namespace: this.namespace }, spec: { apiKeySecretRef: { name: secret, key: 'apiKey' }, teams: teams.map(id => ({ id })) } }); }
    async operation(connection: string, fields: Record<string, unknown>) {
        const name = 'op-' + crypto.randomUUID();
        try {
            await this.request(this.path('operations'), { apiVersion: 'linear.providers.faros.sh/v1alpha1', kind: 'Operation', metadata: { name, namespace: this.namespace }, spec: { connection, ...fields } });
        }
        catch (error) {
            this.signal.throwIfAborted();
            throw new Error(`${name}: submission outcome unknown. Inspect Operations before repeating it. ${error instanceof Error ? error.message : ''}`);
        }
        for (let i = 0; i < 30; i++) {
            const op = await this.request(this.path('operations', name)) as Resource;
            if (op.status?.phase === 'Succeeded')
                return op.status.result || {};
            if (op.status?.phase === 'Failed' || op.status?.phase === 'Uncertain')
                throw new Error(`${name}: ${op.status.message || op.status.phase}. Inspect this operation before submitting another write.`);
            await new Promise<void>((resolve, reject) => { const abort = () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); }; const timer = setTimeout(() => { this.signal.removeEventListener('abort', abort); resolve(); }, 1000); this.signal.addEventListener('abort', abort, { once: true }); });
        }
        throw new Error(`${name} is still pending. Refresh Operations to inspect it before repeating a write.`);
    }
}
