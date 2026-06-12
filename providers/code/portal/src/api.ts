// GraphQL client for the code provider's portal.
//
// Every read and write goes through the hub's embedded GraphQL gateway at
// /graphql/<cluster> — reads as `code_kedge_faros_sh { v1alpha1 { … } }`
// queries, writes as create/update/delete mutations (and applyYaml for
// create-or-update). The shell pushes kedgeContext.tenant (kcp cluster name,
// used as the /graphql path segment) and kedgeContext.token (bearer). The one
// non-gateway call is oauthConfig, which probes the provider backend directly.

import type {
  Collaborator,
  Connection,
  DeployKey,
  ErrorResponse,
  Package,
  PackageRow,
  Repository,
} from './types'

const GROUP = 'code.kedge.faros.sh'
const VERSION = 'v1alpha1'
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

interface KCPMetadata {
  name: string
  uid?: string
  resourceVersion?: string
  creationTimestamp?: string
}
interface KCPCondition {
  type: string
  status: string
  reason?: string
  message?: string
}
interface RawCR {
  metadata: KCPMetadata
  spec?: Record<string, unknown>
  status?: { conditions?: KCPCondition[] } & Record<string, unknown>
}

// graphqlQuery runs a query against the hub's embedded GraphQL gateway at
// /graphql/<cluster> (same origin as the portal). The gateway serves every CRD
// bound in the tenant workspace — including the code provider's — so read-only
// views can pull CRs without a custom REST endpoint. Auth is the caller's own
// bearer token; the workspace is the path segment.
async function graphqlQuery<T>(query: string, variables: Record<string, unknown>): Promise<T> {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
  const res = await fetch('/graphql/' + clusterName, {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: JSON.stringify({ query, variables }),
  })
  const text = await res.text()
  if (!res.ok) {
    throw <ErrorResponse>{ reason: res.status === 404 ? 'NotFound' : 'HTTPError', message: text || res.statusText }
  }
  const body = (text ? JSON.parse(text) : {}) as { data?: T; errors?: { message: string }[] }
  if (body.errors && body.errors.length) {
    throw <ErrorResponse>{ reason: 'GraphQLError', message: body.errors.map(e => e.message).join('; ') }
  }
  return (body.data ?? {}) as T
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

function pkgFromCR(cr: RawCR): Package {
  const status = cr.status ?? {}
  return {
    name: String(status.packageName ?? ''),
    type: String(status.type ?? ''),
    visibility: status.visibility ? String(status.visibility) : undefined,
    htmlURL: status.htmlURL ? String(status.htmlURL) : undefined,
    versionCount: typeof status.versionCount === 'number' ? status.versionCount : undefined,
    updatedAt: status.updatedAt ? String(status.updatedAt) : undefined,
  }
}

// pkgRowFromCR is pkgFromCR plus the owning repository, for the all-packages
// view that spans every repository in the workspace.
function pkgRowFromCR(cr: RawCR): PackageRow {
  return { ...pkgFromCR(cr), repositoryRef: String((cr.spec ?? {}).repositoryRef ?? '') }
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

// ── GraphQL write helpers ──────────────────────────────────────────────────
// All writes go through the gateway's mutation API (no kcp REST proxy). applyCR
// wraps applyYaml, whose server-side create-or-update semantics make writes
// idempotent and handle the "adopt an existing object" case (e.g. a leftover
// credential Secret) without client-side resourceVersion juggling.
async function applyCR(manifest: Record<string, unknown>): Promise<RawCR> {
  const data = await graphqlQuery<{ applyYaml?: unknown }>(
    'mutation($y: String!) { applyYaml(yaml: $y) }',
    { y: JSON.stringify(manifest) },
  )
  // applyYaml returns the applied object as a JSON string (JSONString scalar);
  // tolerate an already-parsed object too.
  const raw = data.applyYaml
  return (typeof raw === 'string' ? JSON.parse(raw || '{}') : raw ?? {}) as RawCR
}

// deleteCR deletes a code-group resource by name via the delete<Kind> mutation.
async function deleteCR(kind: string, name: string): Promise<void> {
  await graphqlQuery(
    `mutation($n: String!) { code_kedge_faros_sh { v1alpha1 { delete${kind}(name: $n) } } }`,
    { n: name },
  )
}

// deleteSecret removes a credential Secret (core/v1, namespaced) by name. Best-
// effort: a missing Secret is not an error for the caller.
async function deleteSecret(name: string): Promise<void> {
  await graphqlQuery(
    'mutation($n: String!, $ns: String!) { v1 { deleteSecret(name: $n, namespace: $ns) } }',
    { n: name, ns: CRED_NAMESPACE },
  )
}

// ── GraphQL read helpers ───────────────────────────────────────────────────
// The gateway returns each CR as a metadata/spec/status object — the same shape
// the kcp REST proxy does — so the *FromCR mappers consume GraphQL items as-is.
// We select the full spec/status the mappers read; the group code.kedge.faros.sh
// is the GraphQL field code_kedge_faros_sh (dots → underscores), list fields are
// the capitalised plural (Connections), single-get is the capitalised singular
// (Connection(name: …)).
const GQL_META = 'metadata { name uid resourceVersion creationTimestamp }'
const GQL_COND = 'conditions { type status reason message }'
const F_CONNECTION = `${GQL_META} spec { provider type owner secretRef { name namespace key } baseURL } status { login scopes ${GQL_COND} }`
const F_REPOSITORY = `${GQL_META} spec { connectionRef name owner visibility description defaultBranch autoInit } status { repoID htmlURL cloneURL sshURL ${GQL_COND} }`
const F_DEPLOYKEY = `${GQL_META} spec { repositoryRef title publicKey readOnly } status { keyID secretRef { name } ${GQL_COND} }`
const F_COLLABORATOR = `${GQL_META} spec { repositoryRef username permission } status { invitationID ${GQL_COND} }`
const F_PACKAGE = `${GQL_META} spec { repositoryRef } status { packageName type visibility htmlURL versionCount updatedAt ${GQL_COND} }`

// gqlList queries a resource's list field and returns the RawCR-shaped items. An
// optional labelselector narrows the set server-side.
async function gqlList(kind: string, fields: string, labelselector?: string): Promise<RawCR[]> {
  const decl = labelselector !== undefined ? '($sel: String!)' : ''
  const arg = labelselector !== undefined ? '(labelselector: $sel)' : ''
  const query = `query${decl} { code_kedge_faros_sh { v1alpha1 { ${kind}${arg} { items { ${fields} } } } } }`
  const data = await graphqlQuery<{ code_kedge_faros_sh?: { v1alpha1?: Record<string, { items?: RawCR[] }> } }>(
    query,
    labelselector !== undefined ? { sel: labelselector } : {},
  )
  return data.code_kedge_faros_sh?.v1alpha1?.[kind]?.items ?? []
}

// gqlGet fetches a single named object (capitalised-singular field). Throws a
// NotFound ErrorResponse when the gateway returns null.
async function gqlGet(kind: string, name: string, fields: string): Promise<RawCR> {
  const query = `query($n: String!) { code_kedge_faros_sh { v1alpha1 { ${kind}(name: $n) { ${fields} } } } }`
  const data = await graphqlQuery<{ code_kedge_faros_sh?: { v1alpha1?: Record<string, RawCR | null> } }>(query, { n: name })
  const obj = data.code_kedge_faros_sh?.v1alpha1?.[kind]
  if (!obj) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
  return obj
}

export const api = {
  // ── Connections ──────────────────────────────────────────────────────────
  async listConnections(): Promise<Connection[]> {
    return (await gqlList('Connections', F_CONNECTION)).map(connFromCR)
  },

  // connect creates the Connection, then the token Secret it references — in
  // that order so the Secret can own-reference the Connection and be garbage-
  // collected with it. type is 'pat' for a pasted token or 'oauth' for one from
  // the GitHub connect flow — same storage, only the credential's origin differs.
  // Idempotent: an existing Connection is adopted and its Secret overwritten,
  // so reconnecting never trips over leftovers from a prior connection.
  async connect(input: { name: string; owner: string; token: string; baseURL?: string; type?: 'pat' | 'oauth' }): Promise<Connection> {
    const name = dns1123(input.name)
    const secretName = name + '-token'
    // 1) Connection referencing the (not-yet-created) Secret.
    const spec: Record<string, unknown> = {
      provider: 'github',
      type: input.type ?? 'pat',
      owner: input.owner,
      secretRef: { name: secretName, namespace: CRED_NAMESPACE, key: TOKEN_KEY },
    }
    if (input.baseURL) spec.baseURL = input.baseURL
    const conn = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Connection',
      metadata: { name },
      spec,
    })
    // 2) Secret holding the token, owned by the Connection so kcp GC removes it
    // with the Connection. applyCR's create-or-update adopts a leftover Secret.
    await applyCR({
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: {
        name: secretName,
        namespace: CRED_NAMESPACE,
        ownerReferences: [{ apiVersion: `${GROUP}/${VERSION}`, kind: 'Connection', name, uid: conn.metadata.uid }],
      },
      type: 'Opaque',
      stringData: { [TOKEN_KEY]: input.token },
    })
    return connFromCR(conn)
  },

  async deleteConnection(name: string): Promise<void> {
    // Resolve the credential Secret first so we can remove it explicitly. The
    // ownerReference would let GC reclaim it, but deleting it here makes cleanup
    // immediate and guarantees the name is free for the next connection.
    let secretName = name + '-token'
    try {
      const conn = await gqlGet('Connection', name, F_CONNECTION)
      const ref = conn.spec?.secretRef as Record<string, unknown> | undefined
      if (ref?.name) secretName = String(ref.name)
    } catch {
      // connection already gone — fall back to the naming convention
    }
    await deleteCR('Connection', name)
    try {
      await deleteSecret(secretName)
    } catch (e) {
      // best-effort: a since-deleted Secret (GC raced us) is fine
      if (!/not\s*found/i.test((e as ErrorResponse).message ?? '')) throw e
    }
  },

  // oauthConfig probes the provider backend (via the hub /services proxy) for
  // whether the "Connect with GitHub" flow is configured. Returns enabled:false
  // (never throws) so the view can silently fall back to the PAT form.
  async oauthConfig(): Promise<{ enabled: boolean; startURL?: string; scopes?: string }> {
    try {
      const headers: Record<string, string> = { Accept: 'application/json' }
      if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
      const res = await fetch('/services/providers/code/oauth/github/config', { headers, credentials: 'same-origin' })
      if (!res.ok) return { enabled: false }
      return (await res.json()) as { enabled: boolean; startURL?: string; scopes?: string }
    } catch {
      return { enabled: false }
    }
  },

  // ── Repositories ─────────────────────────────────────────────────────────
  async listRepositories(): Promise<Repository[]> {
    return (await gqlList('Repositories', F_REPOSITORY)).map(repoFromCR)
  },

  async getRepository(name: string): Promise<Repository> {
    return repoFromCR(await gqlGet('Repository', name, F_REPOSITORY))
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
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Repository',
      metadata: { name },
      spec,
    })
    return repoFromCR(created)
  },

  async deleteRepository(name: string): Promise<void> {
    await deleteCR('Repository', name)
  },

  // updateRepositoryConnection repoints an existing Repository at a different
  // Connection. The update<Kind> mutation is a server-side merge-patch, so only
  // spec.connectionRef changes; the controller re-resolves the new credential/
  // owner on the next reconcile.
  async updateRepositoryConnection(name: string, connectionRef: string): Promise<Repository> {
    const data = await graphqlQuery<{ code_kedge_faros_sh?: { v1alpha1?: { updateRepository?: RawCR } } }>(
      `mutation($n: String!, $ref: String!) {
        code_kedge_faros_sh { v1alpha1 {
          updateRepository(name: $n, object: { spec: { connectionRef: $ref } }) { ${F_REPOSITORY} }
        } }
      }`,
      { n: name, ref: connectionRef },
    )
    const updated = data.code_kedge_faros_sh?.v1alpha1?.updateRepository
    if (!updated) throw <ErrorResponse>{ reason: 'ServerError', message: 'updateRepository returned no object' }
    return repoFromCR(updated)
  },

  // ── Deploy keys ──────────────────────────────────────────────────────────
  async listDeployKeys(repositoryRef: string): Promise<DeployKey[]> {
    return (await gqlList('DeployKeys', F_DEPLOYKEY)).map(keyFromCR).filter(k => k.repositoryRef === repositoryRef)
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
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'DeployKey',
      metadata: { name },
      spec,
    })
    return keyFromCR(created)
  },

  async deleteDeployKey(name: string): Promise<void> {
    await deleteCR('DeployKey', name)
  },

  // ── Collaborators ────────────────────────────────────────────────────────
  async listCollaborators(repositoryRef: string): Promise<Collaborator[]> {
    return (await gqlList('Collaborators', F_COLLABORATOR)).map(collabFromCR).filter(c => c.repositoryRef === repositoryRef)
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
    const created = await applyCR({
      apiVersion: `${GROUP}/${VERSION}`,
      kind: 'Collaborator',
      metadata: { name },
      spec,
    })
    return collabFromCR(created)
  },

  async deleteCollaborator(name: string): Promise<void> {
    await deleteCR('Collaborator', name)
  },

  // ── Packages (read-only) ─────────────────────────────────────────────────
  // Packages are observed host state the code provider's crawler mirrors into
  // Package CRs (one per artifact, owned by the Repository). We read them via
  // the GraphQL gateway — like every other CR — instead of hitting the host on
  // every page view (which GitHub rate-limits). listPackages narrows to one
  // repository by the label the crawler stamps; listAllPackages spans the
  // workspace for the Packages tab.
  async listPackages(repositoryRef: string): Promise<Package[]> {
    return (await gqlList('Packages', F_PACKAGE, `${PACKAGE_REPO_LABEL}=${repositoryRef}`)).map(pkgFromCR)
  },

  async listAllPackages(): Promise<PackageRow[]> {
    return (await gqlList('Packages', F_PACKAGE)).map(pkgRowFromCR)
  },
}

// PACKAGE_REPO_LABEL mirrors codev1alpha1.LabelRepository — the crawler stamps
// it on every Package so we can list one repository's packages by selector.
const PACKAGE_REPO_LABEL = 'code.kedge.faros.sh/repository'

function shortRand(): string {
  // Browser crypto for a short suffix; avoids name collisions without Date/Math.random concerns.
  const a = new Uint8Array(3)
  crypto.getRandomValues(a)
  return Array.from(a, b => b.toString(16).padStart(2, '0')).join('')
}
