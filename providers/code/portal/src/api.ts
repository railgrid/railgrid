// GraphQL client for the code provider's portal.
//
// Every read and write goes through the hub's embedded GraphQL gateway at
// /graphql/<cluster> — reads as `code_faros_sh { v1alpha1 { … } }`
// queries, writes as create/update/delete mutations (and applyYaml for
// create-or-update). The shell pushes farosContext.tenant (kcp cluster name,
// used as the /graphql path segment) and farosContext.token (bearer). The one
// non-gateway call is oauthConfig, which probes the provider backend directly.

import type {
  Collaborator,
  Connection,
  ConnectionDetail,
  DeployKey,
  ErrorResponse,
  Package,
  PackageRow,
  Repository,
  RepositoryDetail,
} from './types'

const GROUP = 'code.faros.sh'
const VERSION = 'v1alpha1'
const CRED_NAMESPACE = 'default'
const TOKEN_KEY = 'token'

let bearerToken: string | null = null
let clusterName: string | null = null
let apiContextGeneration = 0

interface APIRequestContext {
  bearerToken: string | null
  clusterName: string | null
  // Page-owned requests participate in singleton authority invalidation. An
  // explicit immutable context (used by the separate dashboard element) is
  // self-contained and therefore has no page generation.
  generation: number | null
}

export interface APIReadContext {
  token?: string | null
  tenant?: string | null
}

function captureRequestContext(): APIRequestContext {
  return { bearerToken, clusterName, generation: apiContextGeneration }
}

function explicitRequestContext(context: APIReadContext): APIRequestContext {
  return { bearerToken: context.token || null, clusterName: context.tenant || null, generation: null }
}

function assertRequestContext(context: APIRequestContext): void {
  if (context.generation !== null && context.generation !== apiContextGeneration) {
    throw <ErrorResponse>{ reason: 'ContextChanged', message: 'workspace or authentication context changed while the request was in flight' }
  }
}

// setBasePath is a no-op: kcp paths are built from the cluster name, not the
// provider basePath. Kept so App.vue's watcher type-checks.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}
export function setAPIContext(context: APIReadContext): void {
  const nextToken = context.token || null
  const nextTenant = context.tenant || null
  if (nextToken === bearerToken && nextTenant === clusterName) return
  bearerToken = nextToken
  clusterName = nextTenant
  apiContextGeneration += 1
}

interface KCPMetadata {
  name: string
  uid: string
  resourceVersion?: string | null
  generation?: number | null
  creationTimestamp?: string | null
  deletionTimestamp?: string | null
}
interface KCPCondition {
  type: string
  status: string
  reason?: string | null
  message?: string | null
  lastTransitionTime?: string | null
}
interface RawCR {
  apiVersion?: string
  kind?: string
  metadata: KCPMetadata
  spec?: Record<string, unknown>
  status?: ({
    observedGeneration?: number | null
    conditions?: KCPCondition[] | null
  } & Record<string, unknown>) | null
}

function protocolError(message: string): ErrorResponse {
  return { reason: 'ProtocolError', message }
}

function isKubernetesGraphQLNotFound(error: unknown, resource: string, name: string): boolean {
  const value = error as { reason?: string; message?: string }
  return value.reason === 'GraphQLError' && value.message === `${resource} "${name}" not found`
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function hasOwn(value: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function validateOptionalString(value: unknown, label: string): void {
  if (value !== undefined && value !== null && typeof value !== 'string') {
    throw protocolError(`${label} had an invalid shape`)
  }
}

function validateRawCR(
  value: unknown,
  label: string,
  options: { requireSpec?: boolean; requireTypeMeta?: boolean } = {},
): RawCR {
  if (!isRecord(value)) throw protocolError(`${label} was not a resource object`)
  if (options.requireTypeMeta) {
    if (typeof value.apiVersion !== 'string' || !value.apiVersion || typeof value.kind !== 'string' || !value.kind) {
      throw protocolError(`${label} was missing apiVersion or kind`)
    }
  }

  const metadata = value.metadata
  if (!isRecord(metadata) || typeof metadata.name !== 'string' || !metadata.name || typeof metadata.uid !== 'string' || !metadata.uid) {
    throw protocolError(`${label} was missing valid metadata.name or metadata.uid`)
  }
  validateOptionalString(metadata.resourceVersion, `${label} metadata.resourceVersion`)
  validateOptionalString(metadata.creationTimestamp, `${label} metadata.creationTimestamp`)
  validateOptionalString(metadata.deletionTimestamp, `${label} metadata.deletionTimestamp`)
  if (metadata.generation !== undefined && metadata.generation !== null &&
    (typeof metadata.generation !== 'number' || !Number.isSafeInteger(metadata.generation) || metadata.generation < 0)) {
    throw protocolError(`${label} metadata.generation had an invalid shape`)
  }

  if (options.requireSpec && !isRecord(value.spec)) {
    throw protocolError(`${label} was missing an object spec`)
  }
  if (value.spec !== undefined && value.spec !== null && !isRecord(value.spec)) {
    throw protocolError(`${label} spec had an invalid shape`)
  }

  const status = value.status
  if (status !== undefined && status !== null) {
    if (!isRecord(status)) throw protocolError(`${label} status had an invalid shape`)
    if (status.observedGeneration !== undefined && status.observedGeneration !== null &&
      (typeof status.observedGeneration !== 'number' || !Number.isSafeInteger(status.observedGeneration) || status.observedGeneration < 0)) {
      throw protocolError(`${label} status.observedGeneration had an invalid shape`)
    }
    if (status.conditions !== undefined && status.conditions !== null) {
      if (!Array.isArray(status.conditions)) throw protocolError(`${label} status.conditions had an invalid shape`)
      status.conditions.forEach((condition, index) => {
        if (!isRecord(condition) || typeof condition.type !== 'string' || !condition.type || typeof condition.status !== 'string') {
          throw protocolError(`${label} condition ${index} had an invalid shape`)
        }
        validateOptionalString(condition.reason, `${label} condition ${index} reason`)
        validateOptionalString(condition.message, `${label} condition ${index} message`)
        validateOptionalString(condition.lastTransitionTime, `${label} condition ${index} lastTransitionTime`)
      })
    }
  }
  return value as unknown as RawCR
}

type CodeResourceKind = 'Connection' | 'Repository' | 'DeployKey' | 'Collaborator' | 'Package'
type NonRepositoryKind = Exclude<CodeResourceKind, 'Repository'>

function requireResourceString(record: Record<string, unknown>, key: string, label: string): void {
  if (typeof record[key] !== 'string' || !(record[key] as string).trim()) {
    throw protocolError(`${label} was missing a valid ${key}`)
  }
}

function validateOptionalBoolean(value: unknown, label: string): void {
  if (value !== undefined && value !== null && typeof value !== 'boolean') {
    throw protocolError(`${label} had an invalid shape`)
  }
}

function validateOptionalInteger(value: unknown, label: string): void {
  if (value !== undefined && value !== null &&
    (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)) {
    throw protocolError(`${label} had an invalid shape`)
  }
}

function validateCodeResource(
  value: unknown,
  kind: NonRepositoryKind,
  label: string,
  options: { requireTypeMeta?: boolean } = {},
): RawCR {
  const resource = validateRawCR(value, label, { requireSpec: true, requireTypeMeta: options.requireTypeMeta })
  const spec = resource.spec!
  const status = resource.status ?? undefined

  if (kind === 'Connection') {
    requireResourceString(spec, 'provider', `${label} spec`)
    requireResourceString(spec, 'type', `${label} spec`)
    requireResourceString(spec, 'owner', `${label} spec`)
    if (!isRecord(spec.secretRef)) throw protocolError(`${label} spec.secretRef had an invalid shape`)
    requireResourceString(spec.secretRef, 'name', `${label} spec.secretRef`)
    validateOptionalString(spec.secretRef.namespace, `${label} spec.secretRef.namespace`)
    validateOptionalString(spec.secretRef.key, `${label} spec.secretRef.key`)
    validateOptionalString(spec.baseURL, `${label} spec.baseURL`)
    if (status) {
      validateOptionalString(status.login, `${label} status.login`)
      if (status.scopes !== undefined && status.scopes !== null &&
        (!Array.isArray(status.scopes) || status.scopes.some(scope => typeof scope !== 'string'))) {
        throw protocolError(`${label} status.scopes had an invalid shape`)
      }
    }
    return resource
  }

  requireResourceString(spec, 'repositoryRef', `${label} spec`)
  if (kind === 'DeployKey') {
    validateOptionalString(spec.title, `${label} spec.title`)
    validateOptionalString(spec.publicKey, `${label} spec.publicKey`)
    validateOptionalBoolean(spec.readOnly, `${label} spec.readOnly`)
    if (status) {
      validateOptionalString(status.keyID, `${label} status.keyID`)
      if (status.secretRef !== undefined && status.secretRef !== null) {
        if (!isRecord(status.secretRef)) throw protocolError(`${label} status.secretRef had an invalid shape`)
        requireResourceString(status.secretRef, 'name', `${label} status.secretRef`)
      }
    }
  } else if (kind === 'Collaborator') {
    requireResourceString(spec, 'username', `${label} spec`)
    validateOptionalString(spec.permission, `${label} spec.permission`)
    if (status) validateOptionalString(status.invitationID, `${label} status.invitationID`)
  } else if (status) {
    validateOptionalString(status.packageName, `${label} status.packageName`)
    validateOptionalString(status.type, `${label} status.type`)
    validateOptionalString(status.visibility, `${label} status.visibility`)
    validateOptionalString(status.htmlURL, `${label} status.htmlURL`)
    validateOptionalString(status.updatedAt, `${label} status.updatedAt`)
    validateOptionalInteger(status.versionCount, `${label} status.versionCount`)
  }
  return resource
}

function validateRepositoryResource(
  value: unknown,
  label: string,
  options: { requireTypeMeta?: boolean } = {},
): RawCR {
  const resource = validateRawCR(value, label, { requireSpec: true, requireTypeMeta: options.requireTypeMeta })
  const spec = resource.spec!
  requireResourceString(spec, 'connectionRef', `${label} spec`)
  requireResourceString(spec, 'name', `${label} spec`)
  validateOptionalString(spec.owner, `${label} spec.owner`)
  validateOptionalString(spec.visibility, `${label} spec.visibility`)
  validateOptionalString(spec.description, `${label} spec.description`)
  validateOptionalString(spec.defaultBranch, `${label} spec.defaultBranch`)
  validateOptionalBoolean(spec.autoInit, `${label} spec.autoInit`)
  const status = resource.status ?? undefined
  if (status) {
    validateOptionalString(status.repoID, `${label} status.repoID`)
    validateOptionalString(status.htmlURL, `${label} status.htmlURL`)
    validateOptionalString(status.cloneURL, `${label} status.cloneURL`)
    validateOptionalString(status.sshURL, `${label} status.sshURL`)
  }
  return resource
}

function validateResourceForKind(
  value: unknown,
  kind: CodeResourceKind,
  label: string,
  options: { requireTypeMeta?: boolean } = {},
): RawCR {
  return kind === 'Repository'
    ? validateRepositoryResource(value, label, options)
    : validateCodeResource(value, kind, label, options)
}

// graphqlQuery runs a query against the hub's embedded GraphQL gateway at
// /graphql/<cluster> (same origin as the portal). The gateway serves every CRD
// bound in the tenant workspace — including the code provider's — so read-only
// views can pull CRs without a custom REST endpoint. Auth is the caller's own
// bearer token; the workspace is the path segment.
async function graphqlQuery<T>(query: string, variables: Record<string, unknown>, context = captureRequestContext()): Promise<T> {
  assertRequestContext(context)
  if (!context.clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (context.bearerToken) headers['Authorization'] = 'Bearer ' + context.bearerToken
  const res = await fetch('/graphql/' + context.clusterName, {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: JSON.stringify({ query, variables }),
  })
  const text = await res.text()
  assertRequestContext(context)
  if (!res.ok) {
    throw <ErrorResponse>{ reason: 'HTTPError', message: `${res.status}: ${text || res.statusText}` }
  }
  let parsed: unknown
  try {
    parsed = text ? JSON.parse(text) : null
  } catch {
    throw protocolError('GraphQL gateway returned malformed JSON')
  }
  if (!isRecord(parsed)) {
    throw protocolError('GraphQL gateway returned an invalid response envelope')
  }
  const body = parsed as { data?: unknown; errors?: unknown }
  if (hasOwn(parsed, 'errors')) {
    if (!Array.isArray(body.errors)) {
      throw protocolError('GraphQL gateway response included a malformed errors field')
    }
    const messages = body.errors.map((entry, index) => {
      if (!isRecord(entry) || typeof entry.message !== 'string') {
        throw protocolError(`GraphQL gateway response included malformed error entry ${index}`)
      }
      return entry.message
    })
    if (messages.length) {
      throw <ErrorResponse>{ reason: 'GraphQLError', message: messages.join('; ') }
    }
  }
  if (!isRecord(body.data)) {
    throw protocolError('GraphQL gateway response did not include an object data field')
  }
  return body.data as T
}

function condTrue(cr: RawCR, type: string): boolean {
  return (cr.status?.conditions ?? []).some(c => c.type === type && c.status === 'True')
}
function condMsg(cr: RawCR, type: string): string | undefined {
  return (cr.status?.conditions ?? []).find(c => c.type === type)?.message ?? undefined
}

function reconciliationState(cr: RawCR): {
  generation?: number
  observedGeneration?: number
  reconciled: boolean
  waitingMessage?: string
} {
  const generation = typeof cr.metadata.generation === 'number' ? cr.metadata.generation : undefined
  const observedGeneration = typeof cr.status?.observedGeneration === 'number' ? cr.status.observedGeneration : undefined
  const reconciled = generation === undefined || (observedGeneration !== undefined && observedGeneration >= generation)
  return {
    generation,
    observedGeneration,
    reconciled,
    waitingMessage: !reconciled && generation !== undefined
      ? `Waiting for the controller to observe generation ${generation}.`
      : undefined,
  }
}

function connFromCR(cr: RawCR): Connection {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const reconciliation = reconciliationState(cr)
  return {
    name: cr.metadata.name,
    uid: cr.metadata.uid,
    deletionTimestamp: cr.metadata.deletionTimestamp ?? undefined,
    generation: reconciliation.generation,
    observedGeneration: reconciliation.observedGeneration,
    provider: String(spec.provider ?? ''),
    type: String(spec.type ?? ''),
    owner: String(spec.owner ?? ''),
    secretName: String((spec.secretRef as Record<string, unknown> | undefined)?.name ?? ''),
    login: status.login ? String(status.login) : undefined,
    scopes: Array.isArray(status.scopes) ? (status.scopes as string[]) : [],
    validated: reconciliation.reconciled && condTrue(cr, 'Validated'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Validated') ?? condMsg(cr, 'Ready'),
  }
}

// connDetailFromCR is connFromCR plus the raw spec/status the detail view needs
// to explain a pending connection: every condition verbatim, the secret it
// references, and observed-vs-current generation.
function connDetailFromCR(cr: RawCR): ConnectionDetail {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const secretRef = (spec.secretRef as Record<string, unknown> | undefined) ?? {}
  return {
    ...connFromCR(cr),
    baseURL: spec.baseURL ? String(spec.baseURL) : undefined,
    secretNamespace: secretRef.namespace ? String(secretRef.namespace) : undefined,
    secretKey: secretRef.key ? String(secretRef.key) : undefined,
    creationTimestamp: cr.metadata.creationTimestamp ?? undefined,
    conditions: (status.conditions ?? []).map(c => ({
      type: c.type,
      status: c.status,
      reason: c.reason ?? undefined,
      message: c.message ?? undefined,
      lastTransitionTime: c.lastTransitionTime ?? undefined,
    })),
  }
}

function repoFromCR(cr: RawCR): Repository {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const reconciliation = reconciliationState(cr)
  return {
    name: cr.metadata.name,
    uid: cr.metadata.uid,
    deletionTimestamp: cr.metadata.deletionTimestamp ?? undefined,
    generation: reconciliation.generation,
    observedGeneration: reconciliation.observedGeneration,
    connectionRef: String(spec.connectionRef ?? ''),
    repo: String(spec.name ?? ''),
    owner: spec.owner ? String(spec.owner) : undefined,
    visibility: String(spec.visibility ?? 'private'),
    description: spec.description ? String(spec.description) : undefined,
    htmlURL: status.htmlURL ? String(status.htmlURL) : undefined,
    sshURL: status.sshURL ? String(status.sshURL) : undefined,
    cloneURL: status.cloneURL ? String(status.cloneURL) : undefined,
    ready: reconciliation.reconciled && condTrue(cr, 'Ready'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Ready'),
  }
}

// repoDetailFromCR is repoFromCR plus the raw status the detail view needs to
// explain a pending repository: every condition verbatim and observed-vs-current
// generation.
function repoDetailFromCR(cr: RawCR): RepositoryDetail {
  const status = cr.status ?? {}
  return {
    ...repoFromCR(cr),
    repoID: status.repoID ? String(status.repoID) : undefined,
    creationTimestamp: cr.metadata.creationTimestamp ?? undefined,
    conditions: (status.conditions ?? []).map(c => ({
      type: c.type,
      status: c.status,
      reason: c.reason ?? undefined,
      message: c.message ?? undefined,
      lastTransitionTime: c.lastTransitionTime ?? undefined,
    })),
  }
}

function keyFromCR(cr: RawCR): DeployKey {
  const spec = cr.spec ?? {}
  const status = cr.status ?? {}
  const secretRef = status.secretRef as Record<string, unknown> | undefined
  const reconciliation = reconciliationState(cr)
  return {
    name: cr.metadata.name,
    uid: cr.metadata.uid,
    deletionTimestamp: cr.metadata.deletionTimestamp ?? undefined,
    generation: reconciliation.generation,
    observedGeneration: reconciliation.observedGeneration,
    repositoryRef: String(spec.repositoryRef ?? ''),
    title: spec.title ? String(spec.title) : undefined,
    readOnly: Boolean(spec.readOnly),
    generated: !spec.publicKey,
    secretName: secretRef ? String(secretRef.name ?? '') : undefined,
    keyID: status.keyID ? String(status.keyID) : undefined,
    ready: reconciliation.reconciled && condTrue(cr, 'Ready'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Ready'),
  }
}

function pkgFromCR(cr: RawCR): Package {
  const status = cr.status ?? {}
  const reconciliation = reconciliationState(cr)
  return {
    name: String(status.packageName ?? ''),
    uid: cr.metadata.uid,
    deletionTimestamp: cr.metadata.deletionTimestamp ?? undefined,
    generation: reconciliation.generation,
    observedGeneration: reconciliation.observedGeneration,
    type: String(status.type ?? ''),
    visibility: status.visibility ? String(status.visibility) : undefined,
    htmlURL: status.htmlURL ? String(status.htmlURL) : undefined,
    versionCount: typeof status.versionCount === 'number' ? status.versionCount : undefined,
    updatedAt: status.updatedAt ? String(status.updatedAt) : undefined,
    ready: reconciliation.reconciled && condTrue(cr, 'Ready'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Ready'),
  }
}

// pkgRowFromCR is pkgFromCR plus the owning repository, for the all-packages
// view that spans every repository in the workspace.
function pkgRowFromCR(cr: RawCR): PackageRow {
  return { ...pkgFromCR(cr), repositoryRef: String((cr.spec ?? {}).repositoryRef ?? '') }
}

function collabFromCR(cr: RawCR): Collaborator {
  const spec = cr.spec ?? {}
  const reconciliation = reconciliationState(cr)
  return {
    name: cr.metadata.name,
    uid: cr.metadata.uid,
    deletionTimestamp: cr.metadata.deletionTimestamp ?? undefined,
    generation: reconciliation.generation,
    observedGeneration: reconciliation.observedGeneration,
    repositoryRef: String(spec.repositoryRef ?? ''),
    username: String(spec.username ?? ''),
    permission: String(spec.permission ?? 'pull'),
    invitationPending: reconciliation.reconciled && condTrue(cr, 'InvitationPending'),
    ready: reconciliation.reconciled && condTrue(cr, 'Ready'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Ready'),
  }
}

// dns1123 turns arbitrary text into a safe object name.
export function normalizeResourceName(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 253) || 'x'
}

// ── GraphQL write helpers ──────────────────────────────────────────────────
// All writes go through the gateway's mutation API (no kcp REST proxy). applyCR
// wraps applyYaml, whose server-side create-or-update semantics make writes
// idempotent and handle the "adopt an existing object" case (e.g. a leftover
// credential Secret) without client-side resourceVersion juggling.
async function applyCR(manifest: Record<string, unknown>, context?: APIRequestContext): Promise<RawCR> {
  const data = await graphqlQuery<{ applyYaml?: unknown }>(
    'mutation($y: String!) { applyYaml(yaml: $y) }',
    { y: JSON.stringify(manifest) },
    context,
  )
  // applyYaml returns the applied object as a JSON string (JSONString scalar);
  // tolerate an already-parsed object too.
  const raw = data.applyYaml
  if (raw === undefined || raw === null) throw protocolError('applyYaml response was missing')
  try {
    const object = typeof raw === 'string' ? JSON.parse(raw) : raw
    const manifestKind = manifest.kind
    const resource = typeof manifestKind === 'string' &&
      ['Connection', 'Repository', 'DeployKey', 'Collaborator', 'Package'].includes(manifestKind)
      ? validateResourceForKind(object, manifestKind as CodeResourceKind, 'applyYaml response', { requireTypeMeta: true })
      : validateRawCR(object, 'applyYaml response', {
          requireSpec: hasOwn(manifest, 'spec'),
          requireTypeMeta: true,
        })
    const expectedMetadata = isRecord(manifest.metadata) ? manifest.metadata : null
    if (resource.apiVersion !== manifest.apiVersion || resource.kind !== manifest.kind ||
      !expectedMetadata || resource.metadata.name !== expectedMetadata.name) {
      throw protocolError('applyYaml returned a different resource than requested')
    }
    return resource
  } catch (error) {
    if ((error as ErrorResponse).reason === 'ProtocolError') throw error
    throw protocolError('applyYaml returned malformed JSON')
  }
}

// deleteCR deletes a code-group resource by name via the delete<Kind> mutation.
async function deleteCR(kind: string, name: string, context?: APIRequestContext): Promise<void> {
  const field = `delete${kind}`
  const data = await graphqlQuery<unknown>(
    `mutation($n: String!) { code_faros_sh { v1alpha1 { delete${kind}(name: $n) } } }`,
    { n: name },
    context,
  )
  const version = codeVersionPayload(data, field)
  if (!hasOwn(version, field)) {
    throw protocolError(`${field} response was missing ${field}`)
  }
}

// deleteCodeResource makes delete retries safe for the exact Kubernetes
// resource miss emitted by the GraphQL gateway. Capture authority once so a
// concurrent workspace/token switch cannot redirect any part of the request.
async function deleteCodeResource(kind: CodeResourceKind, resource: string, name: string): Promise<void> {
  const context = captureRequestContext()
  try {
    await deleteCR(kind, name, context)
  } catch (error) {
    if (!isKubernetesGraphQLNotFound(error, `${resource}.${GROUP}`, name)) throw error
  }
}

// ── GraphQL read helpers ───────────────────────────────────────────────────
// The gateway returns each CR as a metadata/spec/status object — the same shape
// the kcp REST proxy does — so the *FromCR mappers consume GraphQL items as-is.
// We select the full spec/status the mappers read; the group code.faros.sh
// is the GraphQL field code_faros_sh (dots → underscores), list fields are
// the capitalised plural (Connections), single-get is the capitalised singular
// (Connection(name: …)).
const GQL_META = 'metadata { name uid resourceVersion generation creationTimestamp deletionTimestamp }'
const GQL_COND = 'conditions { type status reason message }'
const F_CONNECTION = `${GQL_META} spec { provider type owner secretRef { name namespace key } baseURL } status { login scopes observedGeneration ${GQL_COND} }`
// Detail fragment: adds generation/observedGeneration and per-condition
// lastTransitionTime so the detail view can explain why a connection is pending.
const F_CONNECTION_DETAIL = `${GQL_META} spec { provider type owner secretRef { name namespace key } baseURL } status { login scopes observedGeneration conditions { type status reason message lastTransitionTime } }`
const F_REPOSITORY = `${GQL_META} spec { connectionRef name owner visibility description defaultBranch autoInit } status { repoID htmlURL cloneURL sshURL observedGeneration ${GQL_COND} }`
// Detail fragment: adds generation/observedGeneration and per-condition
// lastTransitionTime so the detail view can explain why a repository is pending.
const F_REPOSITORY_DETAIL = `${GQL_META} spec { connectionRef name owner visibility description defaultBranch autoInit } status { repoID htmlURL cloneURL sshURL observedGeneration conditions { type status reason message lastTransitionTime } }`
const F_DEPLOYKEY = `${GQL_META} spec { repositoryRef title publicKey readOnly } status { keyID secretRef { name } observedGeneration ${GQL_COND} }`
const F_COLLABORATOR = `${GQL_META} spec { repositoryRef username permission } status { invitationID observedGeneration ${GQL_COND} }`
const F_PACKAGE = `${GQL_META} spec { repositoryRef } status { packageName type visibility htmlURL versionCount updatedAt observedGeneration ${GQL_COND} }`

function codeVersionPayload(data: unknown, label: string): Record<string, unknown> {
  if (!isRecord(data) || !hasOwn(data, 'code_faros_sh') || !isRecord(data.code_faros_sh)) {
    throw protocolError(`${label} response was missing code_faros_sh`)
  }
  const group = data.code_faros_sh
  if (!hasOwn(group, VERSION) || !isRecord(group[VERSION])) {
    throw protocolError(`${label} response was missing ${VERSION}`)
  }
  return group[VERSION]
}

function listResourceKind(kind: string): CodeResourceKind {
  switch (kind) {
    case 'Connections': return 'Connection'
    case 'Repositories': return 'Repository'
    case 'DeployKeys': return 'DeployKey'
    case 'Collaborators': return 'Collaborator'
    case 'Packages': return 'Package'
    default: throw protocolError(`Unsupported code resource list ${kind}`)
  }
}

// gqlList queries a resource's list field and returns the RawCR-shaped items. An
// optional labelselector narrows the set server-side.
async function gqlList(kind: string, fields: string, labelselector?: string, context?: APIRequestContext): Promise<RawCR[]> {
  const decl = labelselector !== undefined ? '($sel: String!)' : ''
  const arg = labelselector !== undefined ? '(labelselector: $sel)' : ''
  const query = `query${decl} { code_faros_sh { v1alpha1 { ${kind}${arg} { items { ${fields} } } } } }`
  const data = await graphqlQuery<unknown>(
    query,
    labelselector !== undefined ? { sel: labelselector } : {},
    context,
  )
  const version = codeVersionPayload(data, `${kind} list`)
  if (!hasOwn(version, kind) || !isRecord(version[kind])) {
    throw protocolError(`${kind} list response was missing ${kind}`)
  }
  const list = version[kind]
  if (!hasOwn(list, 'items') || !Array.isArray(list.items)) {
    throw protocolError(`${kind} list response was missing its items array`)
  }
  const resourceKind = listResourceKind(kind)
  return list.items.map((item, index) => validateResourceForKind(item, resourceKind, `${kind} list item ${index}`))
}

// gqlGet fetches a single named object (capitalised-singular field). The
// gateway may represent absence as either null data or an exact Kubernetes
// GraphQL error; both become the stable portal NotFound contract.
async function gqlGet(kind: CodeResourceKind, resource: string, name: string, fields: string, context?: APIRequestContext): Promise<RawCR> {
  const query = `query($n: String!) { code_faros_sh { v1alpha1 { ${kind}(name: $n) { ${fields} } } } }`
  let data: unknown
  try {
    data = await graphqlQuery<unknown>(query, { n: name }, context)
  } catch (error) {
    if (isKubernetesGraphQLNotFound(error, `${resource}.${GROUP}`, name)) {
      throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
    }
    throw error
  }
  const version = codeVersionPayload(data, `${kind} get`)
  if (!hasOwn(version, kind)) {
    throw protocolError(`${kind} get response was missing ${kind}`)
  }
  const obj = version[kind]
  if (obj === null) throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
  return validateResourceForKind(obj, kind, `${kind} get response`)
}

export const api = {
  // ── Connections ──────────────────────────────────────────────────────────
  async listConnections(context?: APIReadContext): Promise<Connection[]> {
    return (await gqlList('Connections', F_CONNECTION, undefined, context ? explicitRequestContext(context) : undefined)).map(connFromCR)
  },

  // getConnection fetches one Connection with the full spec/status the detail
  // view renders — used to diagnose a connection stuck in "pending".
  async getConnection(name: string): Promise<ConnectionDetail> {
    return connDetailFromCR(await gqlGet('Connection', 'connections', name, F_CONNECTION_DETAIL))
  },

  // connect creates the Connection, then the token Secret it references — in
  // that order so the Secret can own-reference the Connection and be garbage-
  // collected with it. type is 'pat' for a pasted token or 'oauth' for one from
  // the GitHub connect flow — same storage, only the credential's origin differs.
  // Idempotent: an existing Connection is adopted and its Secret overwritten,
  // so reconnecting never trips over leftovers from a prior connection.
  async connect(input: { name: string; owner: string; token: string; baseURL?: string; type?: 'pat' | 'oauth' }): Promise<Connection> {
    const context = captureRequestContext()
    const name = normalizeResourceName(input.name)
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
    }, context)
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
    }, context)
    return connFromCR(conn)
  },

  async deleteConnection(name: string): Promise<void> {
    // The credential Secret has an ownerReference to this Connection. Delete
    // only the owner and let Kubernetes garbage collection remove the Secret;
    // deleting the credential first would leave a live Connection unusable when
    // this mutation fails. An exact Kubernetes miss makes retries idempotent.
    await deleteCodeResource('Connection', 'connections', name)
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
      const body: unknown = await res.json()
      if (!isRecord(body) || typeof body.enabled !== 'boolean') return { enabled: false }
      if ((body.startURL !== undefined && typeof body.startURL !== 'string') ||
        (body.scopes !== undefined && typeof body.scopes !== 'string')) return { enabled: false }
      if (body.enabled && (typeof body.startURL !== 'string' || !body.startURL.trim())) return { enabled: false }
      return {
        enabled: body.enabled,
        startURL: body.startURL as string | undefined,
        scopes: body.scopes as string | undefined,
      }
    } catch {
      return { enabled: false }
    }
  },

  // ── Repositories ─────────────────────────────────────────────────────────
  async listRepositories(context?: APIReadContext): Promise<Repository[]> {
    return (await gqlList('Repositories', F_REPOSITORY, undefined, context ? explicitRequestContext(context) : undefined)).map(repoFromCR)
  },

  async getRepository(name: string): Promise<RepositoryDetail> {
    return repoDetailFromCR(await gqlGet('Repository', 'repositories', name, F_REPOSITORY_DETAIL))
  },

  async createRepository(input: {
    name: string
    connectionRef: string
    repo?: string
    visibility?: string
    description?: string
    autoInit?: boolean
  }): Promise<Repository> {
    const name = normalizeResourceName(input.name)
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
    await deleteCodeResource('Repository', 'repositories', name)
  },

  // updateRepositoryConnection repoints an existing Repository at a different
  // Connection. The update<Kind> mutation is a server-side merge-patch, so only
  // spec.connectionRef changes; the controller re-resolves the new credential/
  // owner on the next reconcile.
  async updateRepositoryConnection(name: string, connectionRef: string): Promise<Repository> {
    const data = await graphqlQuery<unknown>(
      `mutation($n: String!, $ref: String!) {
        code_faros_sh { v1alpha1 {
          updateRepository(name: $n, object: { spec: { connectionRef: $ref } }) { ${F_REPOSITORY} }
        } }
      }`,
      { n: name, ref: connectionRef },
    )
    const version = codeVersionPayload(data, 'updateRepository')
    if (!hasOwn(version, 'updateRepository')) {
      throw protocolError('updateRepository response was missing updateRepository')
    }
    return repoFromCR(validateRepositoryResource(version.updateRepository, 'updateRepository response'))
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
    const name = normalizeResourceName(input.repositoryRef + '-' + (input.title || 'key') + '-' + shortRand())
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
    await deleteCodeResource('DeployKey', 'deploykeys', name)
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
    const name = normalizeResourceName(input.repositoryRef + '-' + input.username)
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
    await deleteCodeResource('Collaborator', 'collaborators', name)
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
const PACKAGE_REPO_LABEL = 'code.faros.sh/repository'

function shortRand(): string {
  // Browser crypto for a short suffix; avoids name collisions without Date/Math.random concerns.
  const a = new Uint8Array(3)
  crypto.getRandomValues(a)
  return Array.from(a, b => b.toString(16).padStart(2, '0')).join('')
}
