// kcp-native client for the code provider's portal.
//
// Talks to kcp directly through the hub's kcp REST proxy:
//   /clusters/<tenant>/apis/code.kedge.faros.sh/v1alpha1/<resource>   (our CRDs)
//   /clusters/<tenant>/api/v1/namespaces/default/secrets              (PAT secret)
//
// The shell pushes kedgeContext.tenant (kcp cluster name) and
// kedgeContext.token (bearer). All four code CRDs are cluster-scoped, so their
// CRs live at <cluster>/apis/<g>/<v>/<plural>/<name> with no namespace segment.

import type {
  Collaborator,
  Connection,
  DeployKey,
  ErrorResponse,
  Repository,
} from './types'

const GROUP = 'code.kedge.faros.sh'
const VERSION = 'v1alpha1'
const APIS_PREFIX = `/apis/${GROUP}/${VERSION}`
const CRED_NAMESPACE = 'default'
const TOKEN_KEY = 'token'

let bearerToken: string | null = null
let clusterName: string | null = null

// setBasePath is a no-op: kcp paths are built from the cluster name, not the
// provider basePath. Kept so App.vue's watcher type-checks.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}
export function setToken(token?: string | null) {
  bearerToken = token || null
}
export function setTenant(name?: string | null) {
  clusterName = name || null
}

function clusterBase(): string {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return `/clusters/${clusterName}`
}
function apisBase(): string {
  return clusterBase() + APIS_PREFIX
}

interface KCPMetadata {
  name: string
  uid?: string
  creationTimestamp?: string
}
interface KCPCondition {
  type: string
  status: string
  reason?: string
  message?: string
}
interface KCPList<T> {
  items: T[]
}
interface RawCR {
  metadata: KCPMetadata
  spec?: Record<string, unknown>
  status?: { conditions?: KCPCondition[] } & Record<string, unknown>
}

async function kcpFetch<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body) headers['Content-Type'] = 'application/json'
  if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  if (!res.ok) {
    let reason = 'HTTPError'
    let message = text || res.statusText
    try {
      const parsed = JSON.parse(text) as { reason?: string; message?: string }
      if (parsed && (parsed.reason || parsed.message)) {
        reason = parsed.reason || reason
        message = parsed.message || message
      }
    } catch {
      // non-JSON body — keep raw text
    }
    if (res.status === 404) reason = 'NotFound'
    else if (res.status === 403 && /APIBinding|not\s+found/i.test(message)) reason = 'APIBindingMissing'
    throw <ErrorResponse>{ reason, message }
  }
  return (text ? JSON.parse(text) : null) as T
}

function condTrue(cr: RawCR, type: string): boolean {
  return (cr.status?.conditions ?? []).some(c => c.type === type && c.status === 'True')
}
function condMsg(cr: RawCR, type: string): string | undefined {
  return (cr.status?.conditions ?? []).find(c => c.type === type)?.message
}

function connFromCR(cr: RawCR): Connection {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  return {
    name: cr.metadata.name,
    provider: String(spec.provider ?? ''),
    type: String(spec.type ?? ''),
    owner: String(spec.owner ?? ''),
    secretName: String((spec.secretRef as Record<string, unknown> | undefined)?.name ?? ''),
    login: status.login ? String(status.login) : undefined,
    scopes: Array.isArray(status.scopes) ? (status.scopes as string[]) : [],
    validated: condTrue(cr, 'Validated'),
    message: condMsg(cr, 'Validated') ?? condMsg(cr, 'Ready'),
  }
}

function repoFromCR(cr: RawCR): Repository {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  return {
    name: cr.metadata.name,
    connectionRef: String(spec.connectionRef ?? ''),
    repo: String(spec.name ?? ''),
    owner: spec.owner ? String(spec.owner) : undefined,
    visibility: String(spec.visibility ?? 'private'),
    description: spec.description ? String(spec.description) : undefined,
    htmlURL: status.htmlURL ? String(status.htmlURL) : undefined,
    sshURL: status.sshURL ? String(status.sshURL) : undefined,
    cloneURL: status.cloneURL ? String(status.cloneURL) : undefined,
    ready: condTrue(cr, 'Ready'),
    message: condMsg(cr, 'Ready'),
  }
}

function keyFromCR(cr: RawCR): DeployKey {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const secretRef = status.secretRef as Record<string, unknown> | undefined
  return {
    name: cr.metadata.name,
    repositoryRef: String(spec.repositoryRef ?? ''),
    title: spec.title ? String(spec.title) : undefined,
    readOnly: Boolean(spec.readOnly),
    generated: !spec.publicKey,
    secretName: secretRef ? String(secretRef.name ?? '') : undefined,
    keyID: status.keyID ? String(status.keyID) : undefined,
    ready: condTrue(cr, 'Ready'),
    message: condMsg(cr, 'Ready'),
  }
}

function collabFromCR(cr: RawCR): Collaborator {
  const spec = cr.spec ?? {}
  return {
    name: cr.metadata.name,
    repositoryRef: String(spec.repositoryRef ?? ''),
    username: String(spec.username ?? ''),
    permission: String(spec.permission ?? 'pull'),
    invitationPending: condTrue(cr, 'InvitationPending'),
    ready: condTrue(cr, 'Ready'),
    message: condMsg(cr, 'Ready'),
  }
}

// dns1123 turns arbitrary text into a safe object name.
function dns1123(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 253) || 'x'
}

export const api = {
  // ── Connections ──────────────────────────────────────────────────────────
  async listConnections(): Promise<Connection[]> {
    const l = await kcpFetch<KCPList<RawCR>>('GET', apisBase() + '/connections')
    return (l.items ?? []).map(connFromCR)
  },

  // connect creates the PAT Secret then the Connection that references it.
  async connect(input: { name: string; owner: string; token: string; baseURL?: string }): Promise<Connection> {
    const name = dns1123(input.name)
    const secretName = name + '-token'
    // 1) Secret holding the PAT.
    await kcpFetch<unknown>(
      'POST',
      clusterBase() + `/api/v1/namespaces/${CRED_NAMESPACE}/secrets`,
      {
        apiVersion: 'v1',
        kind: 'Secret',
        metadata: { name: secretName, namespace: CRED_NAMESPACE },
        type: 'Opaque',
        stringData: { [TOKEN_KEY]: input.token },
      },
    )
    // 2) Connection referencing it.
    const spec: Record<string, unknown> = {
      provider: 'github',
      type: 'pat',
      owner: input.owner,
      secretRef: { name: secretName, namespace: CRED_NAMESPACE, key: TOKEN_KEY },
    }
    if (input.baseURL) spec.baseURL = input.baseURL
    const created = await kcpFetch<RawCR>('POST', apisBase() + '/connections', {
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Connection',
      metadata: { name },
      spec,
    })
    return connFromCR(created)
  },

  async deleteConnection(name: string): Promise<void> {
    await kcpFetch<unknown>('DELETE', apisBase() + '/connections/' + encodeURIComponent(name))
  },

  // ── Repositories ─────────────────────────────────────────────────────────
  async listRepositories(): Promise<Repository[]> {
    const l = await kcpFetch<KCPList<RawCR>>('GET', apisBase() + '/repositories')
    return (l.items ?? []).map(repoFromCR)
  },

  async getRepository(name: string): Promise<Repository> {
    const cr = await kcpFetch<RawCR>('GET', apisBase() + '/repositories/' + encodeURIComponent(name))
    return repoFromCR(cr)
  },

  async createRepository(input: {
    name: string
    connectionRef: string
    repo?: string
    visibility?: string
    description?: string
    autoInit?: boolean
  }): Promise<Repository> {
    const name = dns1123(input.name)
    const spec: Record<string, unknown> = {
      connectionRef: input.connectionRef,
      name: input.repo || input.name,
    }
    if (input.visibility) spec.visibility = input.visibility
    if (input.description) spec.description = input.description
    if (input.autoInit) spec.autoInit = true
    const created = await kcpFetch<RawCR>('POST', apisBase() + '/repositories', {
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Repository',
      metadata: { name },
      spec,
    })
    return repoFromCR(created)
  },

  async deleteRepository(name: string): Promise<void> {
    await kcpFetch<unknown>('DELETE', apisBase() + '/repositories/' + encodeURIComponent(name))
  },

  // ── Deploy keys ──────────────────────────────────────────────────────────
  async listDeployKeys(repositoryRef: string): Promise<DeployKey[]> {
    const l = await kcpFetch<KCPList<RawCR>>('GET', apisBase() + '/deploykeys')
    return (l.items ?? []).map(keyFromCR).filter(k => k.repositoryRef === repositoryRef)
  },

  async createDeployKey(input: {
    repositoryRef: string
    title?: string
    publicKey?: string
    readOnly?: boolean
  }): Promise<DeployKey> {
    const name = dns1123(input.repositoryRef + '-' + (input.title || 'key') + '-' + shortRand())
    const spec: Record<string, unknown> = { repositoryRef: input.repositoryRef }
    if (input.title) spec.title = input.title
    if (input.publicKey) spec.publicKey = input.publicKey
    if (input.readOnly) spec.readOnly = true
    const created = await kcpFetch<RawCR>('POST', apisBase() + '/deploykeys', {
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'DeployKey',
      metadata: { name },
      spec,
    })
    return keyFromCR(created)
  },

  async deleteDeployKey(name: string): Promise<void> {
    await kcpFetch<unknown>('DELETE', apisBase() + '/deploykeys/' + encodeURIComponent(name))
  },

  // ── Collaborators ────────────────────────────────────────────────────────
  async listCollaborators(repositoryRef: string): Promise<Collaborator[]> {
    const l = await kcpFetch<KCPList<RawCR>>('GET', apisBase() + '/collaborators')
    return (l.items ?? []).map(collabFromCR).filter(c => c.repositoryRef === repositoryRef)
  },

  async createCollaborator(input: {
    repositoryRef: string
    username: string
    permission?: string
  }): Promise<Collaborator> {
    const name = dns1123(input.repositoryRef + '-' + input.username)
    const spec: Record<string, unknown> = {
      repositoryRef: input.repositoryRef,
      username: input.username,
    }
    if (input.permission) spec.permission = input.permission
    const created = await kcpFetch<RawCR>('POST', apisBase() + '/collaborators', {
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Collaborator',
      metadata: { name },
      spec,
    })
    return collabFromCR(created)
  },

  async deleteCollaborator(name: string): Promise<void> {
    await kcpFetch<unknown>('DELETE', apisBase() + '/collaborators/' + encodeURIComponent(name))
  },
}

function shortRand(): string {
  // Browser crypto for a short suffix; avoids name collisions without Date/Math.random concerns.
  const a = new Uint8Array(3)
  crypto.getRandomValues(a)
  return Array.from(a, b => b.toString(16).padStart(2, '0')).join('')
}
