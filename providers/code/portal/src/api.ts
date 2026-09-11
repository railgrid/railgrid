// Kubernetes REST client for the code provider's portal.
//
// Every read and write goes through the hub's kcp proxy at
// /clusters/<cluster>/apis/code.faros.sh/v1alpha1/… — plain Kubernetes wire
// shapes: List envelopes for reads, server-side apply for create-or-update,
// merge-patch for targeted updates, DELETE with a Status body on failure. The
// shell pushes farosContext.tenant (kcp cluster name, used as the /clusters
// path segment) and farosContext.token (bearer). The one non-kcp call is
// oauthConfig, which probes the provider backend directly.

import {
  createKubeClient,
  isKubeError,
  isKubeNotFound,
  type KubeClient,
  type KubeError,
  type KubeResourceRef,
} from './portalkit/kube'
import type {
  Collaborator,
  Connection,
  ConnectionDetail,
  DeployKey,
  ErrorResponse,
  KubernetesListOptions,
  KubernetesListPage,
  Package,
  PackageRow,
  Repository,
  RepositoryDetail,
} from './types'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'

export type { KubernetesListOptions, KubernetesListPage } from './types'

const GROUP = 'code.faros.sh'
const VERSION = 'v1alpha1'
const CRED_NAMESPACE = 'default'
const TOKEN_KEY = 'token'
// FIELD_MANAGER names this portal as the server-side-apply owner of the fields
// it writes, so a later apply from the same portal can change them without
// force-taking ownership from another manager.
const FIELD_MANAGER = 'provider-code'

type CodeResourceKind = 'Connection' | 'Repository' | 'DeployKey' | 'Collaborator' | 'Package'
type NonRepositoryKind = Exclude<CodeResourceKind, 'Repository'>

// CODE_RESOURCES maps each Kind to its plural REST segment (the CRD `plural`
// under providers/code/config/crds). Code-group resources are cluster-scoped
// within the workspace.
const CODE_RESOURCES: Record<CodeResourceKind, KubeResourceRef> = {
  Connection: { group: GROUP, version: VERSION, resource: 'connections' },
  Repository: { group: GROUP, version: VERSION, resource: 'repositories' },
  DeployKey: { group: GROUP, version: VERSION, resource: 'deploykeys' },
  Collaborator: { group: GROUP, version: VERSION, resource: 'collaborators' },
  Package: { group: GROUP, version: VERSION, resource: 'packages' },
}
// SECRETS is the core-group ref for the credential Secret a Connection points at.
const SECRETS: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }

function isCodeResourceKind(kind: unknown): kind is CodeResourceKind {
  return typeof kind === 'string' && Object.prototype.hasOwnProperty.call(CODE_RESOURCES, kind)
}


let bearerToken: string | null = null
let clusterName: string | null = null
let providerBasePath: string | null = null
let callerUser: string | null = null
// hostFetch is the host-owned transport from farosContext.fetch. When set, the
// host injects Authorization itself and bearerToken is only a change signal;
// providerFetch falls back to the global fetch + bearerToken on older hosts.
let hostFetch: ProviderFetch | null = null
let apiContextGeneration = 0

interface APIRequestContext {
  bearerToken: string | null
  fetch: ProviderFetch
  clusterName: string | null
  // Page-owned requests participate in singleton authority invalidation. An
  // explicit immutable context (used by the separate dashboard element) is
  // self-contained and therefore has no page generation.
  generation: number | null
}

export interface APIReadContext {
  fetch?: ProviderFetch | null
  token?: string | null
  tenant?: string | null
  user?: string | null
}

function captureRequestContext(): APIRequestContext {
  return {
    bearerToken,
    fetch: providerFetch({ fetch: hostFetch, token: bearerToken }),
    clusterName,
    generation: apiContextGeneration,
  }
}

function explicitRequestContext(context: APIReadContext): APIRequestContext {
  return {
    bearerToken: context.token || null,
    fetch: providerFetch(context),
    clusterName: context.tenant || null,
    generation: null,
  }
}

function assertRequestContext(context: APIRequestContext): void {
  if (context.generation !== null && context.generation !== apiContextGeneration) {
    throw <ErrorResponse>{ reason: 'ContextChanged', message: 'workspace or authentication context changed while the request was in flight' }
  }
}

// The kube REST path is built from the cluster name, not the provider basePath,
// but basePath is still part of the shell authority. Track it so in-flight
// requests are fenced when the host switches provider roots.
export function setBasePath(ctxBasePath?: string | null) {
  const nextBasePath = ctxBasePath || null
  if (nextBasePath === providerBasePath) return
  providerBasePath = nextBasePath
  apiContextGeneration += 1
}
export function setAPIContext(context: APIReadContext): void {
  const nextToken = context.token || null
  const nextTenant = context.tenant || null
  const nextUser = context.user || null
  hostFetch = context.fetch ?? null
  if (nextToken === bearerToken && nextTenant === clusterName && nextUser === callerUser) return
  bearerToken = nextToken
  clusterName = nextTenant
  callerUser = nextUser
  apiContextGeneration += 1
}

interface KCPMetadata {
  name: string
  uid: string
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

// isKubeObjectNotFound reports a 404 Status for exactly the named object of the
// requested resource — details.name must match, and details.group/kind must
// agree when the server filled them in. A 404 for the resource *type* (no
// APIBinding yet) or for some other object referenced during admission is not
// an absence of this object and must never be swallowed.
function isKubeObjectNotFound(error: unknown, ref: KubeResourceRef, name: string): boolean {
  if (!isKubeNotFound(error)) return false
  const details = (error as KubeError).body?.details
  if (!details || details.name !== name) return false
  if (details.group !== undefined && details.group !== ref.group) return false
  if (details.kind !== undefined && details.kind !== ref.resource) return false
  return true
}

// kubeErrorResponse maps a transport-level KubeError onto the {reason, message}
// contract the views render. Client-side protocol failures (the vendored client
// reports them with the successful HTTP status and no Status body) become
// ProtocolError; every server failure keeps its HTTP status in the message.
function kubeErrorResponse(error: KubeError): ErrorResponse {
  if (error.status >= 200 && error.status < 300) return protocolError(error.message)
  return { reason: 'HTTPError', message: `${error.status}: ${error.message}` }
}

// kubeCall runs one client request and normalizes its failure. Non-kube
// failures (ContextChanged, fetch rejections) pass through untouched.
async function kubeCall<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run()
  } catch (error) {
    if (isKubeError(error)) throw kubeErrorResponse(error)
    throw error
  }
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

// kubeClientFor builds a kube REST client bound to one captured request
// context: the host-owned transport (which injects Authorization) and the
// tenant's kcp cluster as the /clusters/<cluster> path segment. The client's
// onResponse hook re-asserts the context after every response body is read, so
// a workspace or token switch mid-flight fails the request instead of letting a
// stale answer land in the new context.
function kubeClientFor(context: APIRequestContext): KubeClient {
  assertRequestContext(context)
  if (!context.clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return createKubeClient({
    fetch: context.fetch,
    cluster: context.clusterName,
    fieldManager: FIELD_MANAGER,
    onResponse: () => assertRequestContext(context),
  })
}

function condTrue(cr: RawCR, type: string): boolean {
  return (cr.status?.conditions ?? []).some(c => c.type === type && c.status === 'True')
}
function condFalse(cr: RawCR, type: string): boolean {
  return (cr.status?.conditions ?? []).some(c => c.type === type && c.status === 'False')
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
    defaultBranch: spec.defaultBranch ? String(spec.defaultBranch) : undefined,
    htmlURL: status.htmlURL ? String(status.htmlURL) : undefined,
    cloneURL: status.cloneURL ? String(status.cloneURL) : undefined,
    sshURL: status.sshURL ? String(status.sshURL) : undefined,
    ready: reconciliation.reconciled && condTrue(cr, 'Ready'),
    failed: reconciliation.reconciled && condFalse(cr, 'Ready'),
    message: reconciliation.waitingMessage ?? condMsg(cr, 'Ready'),
  }
}

// repoDetailFromCR is repoFromCR plus the provider health facts the conditions
// section needs: the provider repository ID and every condition verbatim.
function repoDetailFromCR(cr: RawCR): RepositoryDetail {
  const status = cr.status ?? {}
  return {
    ...repoFromCR(cr),
    repoID: status.repoID ? String(status.repoID) : undefined,
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

// ── Kubernetes write helpers ───────────────────────────────────────────────
// refForManifest resolves the REST ref an apply targets from the manifest's
// apiVersion/kind: the code group's CRDs, or the core-group Secret that holds a
// Connection's credential.
function refForManifest(manifest: Record<string, unknown>): KubeResourceRef {
  if (isCodeResourceKind(manifest.kind) && manifest.apiVersion === `${GROUP}/${VERSION}`) {
    return CODE_RESOURCES[manifest.kind]
  }
  if (manifest.kind === 'Secret' && manifest.apiVersion === 'v1') return SECRETS
  throw protocolError(`apply does not support ${String(manifest.apiVersion)} ${String(manifest.kind)}`)
}

// applyCR is server-side apply (create-or-update). Its idempotent semantics
// handle the "adopt an existing object" case (e.g. a leftover credential
// Secret) without client-side resourceVersion juggling; the portal's field
// manager owns exactly the fields the manifest carries.
async function applyCR(manifest: Record<string, unknown>, context = captureRequestContext()): Promise<RawCR> {
  const ref = refForManifest(manifest)
  const client = kubeClientFor(context)
  const object = await kubeCall(() => client.apply(ref, manifest as never))
  const resource = isCodeResourceKind(manifest.kind)
    ? validateResourceForKind(object, manifest.kind, 'apply response', { requireTypeMeta: true })
    : validateRawCR(object, 'apply response', {
        requireSpec: hasOwn(manifest, 'spec'),
        requireTypeMeta: true,
      })
  const expectedMetadata = isRecord(manifest.metadata) ? manifest.metadata : null
  if (resource.apiVersion !== manifest.apiVersion || resource.kind !== manifest.kind ||
    !expectedMetadata || resource.metadata.name !== expectedMetadata.name) {
    throw protocolError('apply returned a different resource than requested')
  }
  return resource
}

// deleteCodeResource deletes a code-group resource by name. An exact
// Kubernetes miss for that object makes retries idempotent; any other failure
// (a type-level 404, a lookalike miss for a different object, a 5xx) surfaces.
// Capture authority once so a concurrent workspace/token switch cannot redirect
// any part of the request.
async function deleteCodeResource(kind: CodeResourceKind, name: string): Promise<void> {
  const context = captureRequestContext()
  const ref = CODE_RESOURCES[kind]
  const client = kubeClientFor(context)
  try {
    await client.delete(ref, name)
  } catch (error) {
    if (isKubeObjectNotFound(error, ref, name)) return
    if (isKubeError(error)) throw kubeErrorResponse(error)
    throw error
  }
}

// ── Kubernetes read helpers ────────────────────────────────────────────────
// kcp returns each CR as a metadata/spec/status object, which the *FromCR
// mappers consume as-is after per-item shape validation. Lists carry the
// standard List envelope (metadata.continue / remainingItemCount /
// resourceVersion), which the vendored client flattens onto the page.

const DEFAULT_LIST_LIMIT = 100
const MAX_LIST_PAGES = 100

interface RawListPage {
  items: RawCR[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

function requestReadContext(context?: APIReadContext): APIRequestContext {
  return context === undefined ? captureRequestContext() : explicitRequestContext(context)
}

function validateListOptions(options: KubernetesListOptions | undefined, label: string): void {
  if (options === undefined) return
  if (options.limit !== undefined &&
    (typeof options.limit !== 'number' || !Number.isSafeInteger(options.limit) || options.limit <= 0)) {
    throw protocolError(`${label} limit had an invalid shape`)
  }
  if (options.continue !== undefined && typeof options.continue !== 'string') {
    throw protocolError(`${label} continue had an invalid shape`)
  }
}

function mapListPage<T>(page: RawListPage, map: (item: RawCR) => T): KubernetesListPage<T> {
  return {
    items: page.items.map(map),
    continue: page.continue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

// kubeListPage fetches one Kubernetes list page. limit and continue are
// optional query parameters, so an undefined option is simply omitted and the
// same call serves both the first and every following page. The vendored
// client already rejects "remaining items but no continue token"; the
// remaining consistency and shape checks live here so a malformed page fails
// closed rather than rendering as complete.
async function kubeListPage(
  kind: CodeResourceKind,
  labelSelector: string | undefined,
  options: KubernetesListOptions | undefined,
  context: APIRequestContext,
): Promise<RawListPage> {
  const label = `${kind} list`
  validateListOptions(options, label)
  const client = kubeClientFor(context)
  const page = await kubeCall(() => client.list(CODE_RESOURCES[kind], {
    labelSelector,
    limit: options?.limit,
    continue: options?.continue,
  }))

  validateOptionalInteger(page.remainingItemCount, `${label} response remainingItemCount`)
  const items = page.items.map((item, index) => validateResourceForKind(item, kind, `${label} item ${index}`))
  const nextContinue = page.continue
  if (typeof page.remainingItemCount === 'number' &&
    ((page.remainingItemCount > 0 && nextContinue === undefined) || (page.remainingItemCount === 0 && nextContinue !== undefined))) {
    throw protocolError(`${label} response had inconsistent continue and remainingItemCount metadata`)
  }
  return {
    items,
    continue: nextContinue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

// kubeListAll walks every page of a workspace-wide (or label-selected) list.
// Opaque cursor repetition and unbounded streams are protocol failures;
// returning a partial aggregate would make the UI silently incomplete.
async function kubeListAll(
  kind: CodeResourceKind,
  labelSelector: string | undefined,
  context: APIRequestContext,
): Promise<RawCR[]> {
  const items: RawCR[] = []
  const seenContinueTokens = new Set<string>()
  let continueToken: string | undefined
  for (let pageNumber = 0; pageNumber < MAX_LIST_PAGES; pageNumber += 1) {
    const page = await kubeListPage(
      kind,
      labelSelector,
      continueToken === undefined
        ? { limit: DEFAULT_LIST_LIMIT }
        : { limit: DEFAULT_LIST_LIMIT, continue: continueToken },
      context,
    )
    items.push(...page.items)
    if (!page.continue) return items
    if (seenContinueTokens.has(page.continue)) {
      throw protocolError(`${kind} list response repeated a continue token`)
    }
    seenContinueTokens.add(page.continue)
    continueToken = page.continue
  }
  throw protocolError(`${kind} list exceeded the maximum page count`)
}

// kubeList is the unpaged list (no limit, so kcp answers in one response) for
// the small per-repository sets the detail view filters client-side.
async function kubeList(kind: CodeResourceKind, labelSelector?: string, context = captureRequestContext()): Promise<RawCR[]> {
  const client = kubeClientFor(context)
  const page = await kubeCall(() => client.list(CODE_RESOURCES[kind], { labelSelector }))
  return page.items.map((item, index) => validateResourceForKind(item, kind, `${kind} list item ${index}`))
}

// kubeGet fetches a single named object. Only an exact Kubernetes miss for
// that object becomes the stable portal NotFound contract; a type-level 404
// or a miss for some other object stays an HTTPError so the view does not
// mistake it for deletion.
async function kubeGet(kind: CodeResourceKind, name: string, context = captureRequestContext()): Promise<RawCR> {
  const ref = CODE_RESOURCES[kind]
  const client = kubeClientFor(context)
  let object: unknown
  try {
    object = await client.get(ref, name)
  } catch (error) {
    if (isKubeObjectNotFound(error, ref, name)) {
      throw <ErrorResponse>{ reason: 'NotFound', message: `${kind} "${name}" not found` }
    }
    if (isKubeError(error)) throw kubeErrorResponse(error)
    throw error
  }
  return validateResourceForKind(object, kind, `${kind} get response`)
}

export const api = {
  // ── Connections ──────────────────────────────────────────────────────────
  async listConnectionsPage(
    options: KubernetesListOptions = {},
    context?: APIReadContext,
  ): Promise<KubernetesListPage<Connection>> {
    return mapListPage(
      await kubeListPage('Connection', undefined, options, requestReadContext(context)),
      connFromCR,
    )
  },

  async listConnections(context?: APIReadContext): Promise<Connection[]> {
    return (await kubeListAll('Connection', undefined, requestReadContext(context))).map(connFromCR)
  },

  // getConnection fetches one Connection with the full spec/status the detail
  // view renders — used to diagnose a connection stuck in "pending".
  async getConnection(name: string): Promise<ConnectionDetail> {
    return connDetailFromCR(await kubeGet('Connection', name))
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
    // this request fails. An exact Kubernetes miss makes retries idempotent.
    await deleteCodeResource('Connection', name)
  },

  // oauthConfig probes the provider backend (via the hub /services proxy) for
  // whether the "Connect with GitHub" flow is configured. A valid disabled
  // response is expected when no OAuth app is configured, but transport,
  // status, and response-shape failures must reach the view so it can offer a
  // retry instead of reporting a false "not configured" state.
  async oauthConfig(): Promise<{ enabled: boolean; startURL?: string; scopes?: string }> {
    const context = captureRequestContext()
    assertRequestContext(context)
    const headers: Record<string, string> = { Accept: 'application/json' }
    const res = await context.fetch('/services/providers/code/oauth/github/config', { headers, credentials: 'same-origin' })
    if (!res.ok) {
      const text = await res.text()
      assertRequestContext(context)
      throw <ErrorResponse>{ reason: 'HTTPError', message: `${res.status}: ${text || res.statusText}` }
    }
    let body: unknown
    try {
      body = await res.json()
    } catch {
      assertRequestContext(context)
      throw protocolError('OAuth configuration returned malformed JSON')
    }
    assertRequestContext(context)
    if (!isRecord(body) || typeof body.enabled !== 'boolean') {
      throw protocolError('OAuth configuration response had an invalid shape')
    }
    if ((body.startURL !== undefined && typeof body.startURL !== 'string') ||
      (body.scopes !== undefined && typeof body.scopes !== 'string')) {
      throw protocolError('OAuth configuration response had an invalid shape')
    }
    if (body.enabled && (typeof body.startURL !== 'string' || !body.startURL.trim())) {
      throw protocolError('OAuth configuration response was enabled but missing a start URL')
    }
    if (!body.enabled) return { enabled: false }
    return {
      enabled: true,
      startURL: body.startURL as string,
      scopes: body.scopes as string | undefined,
    }
  },

  // ── Repositories ─────────────────────────────────────────────────────────
  async listRepositoriesPage(
    options: KubernetesListOptions = {},
    context?: APIReadContext,
  ): Promise<KubernetesListPage<Repository>> {
    return mapListPage(
      await kubeListPage('Repository', undefined, options, requestReadContext(context)),
      repoFromCR,
    )
  },

  async listRepositories(context?: APIReadContext): Promise<Repository[]> {
    return (await kubeListAll('Repository', undefined, requestReadContext(context))).map(repoFromCR)
  },

  async getRepository(name: string): Promise<RepositoryDetail> {
    return repoDetailFromCR(await kubeGet('Repository', name))
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
    await deleteCodeResource('Repository', name)
  },

  // updateRepositoryConnection repoints an existing Repository at a different
  // Connection. A merge-patch touches only spec.connectionRef; the controller
  // re-resolves the new credential/owner on the next reconcile.
  async updateRepositoryConnection(name: string, connectionRef: string): Promise<Repository> {
    const client = kubeClientFor(captureRequestContext())
    const object = await kubeCall(() => client.patch(CODE_RESOURCES.Repository, name, { spec: { connectionRef } }))
    return repoFromCR(validateRepositoryResource(object, 'updateRepository response'))
  },

  // ── Deploy keys ──────────────────────────────────────────────────────────
  async listDeployKeys(repositoryRef: string): Promise<DeployKey[]> {
    return (await kubeList('DeployKey')).map(keyFromCR).filter(k => k.repositoryRef === repositoryRef)
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
    await deleteCodeResource('DeployKey', name)
  },

  // ── Collaborators ────────────────────────────────────────────────────────
  async listCollaborators(repositoryRef: string): Promise<Collaborator[]> {
    return (await kubeList('Collaborator')).map(collabFromCR).filter(c => c.repositoryRef === repositoryRef)
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
    await deleteCodeResource('Collaborator', name)
  },

  // ── Packages (read-only) ─────────────────────────────────────────────────
  // Packages are observed host state the code provider's crawler mirrors into
  // Package CRs (one per artifact, owned by the Repository). We read them from
  // kcp — like every other CR — instead of hitting the host on every page view
  // (which GitHub rate-limits). listPackages narrows to one repository by the
  // label the crawler stamps; listAllPackages spans the workspace for the
  // Packages tab.
  async listPackagesPage(
    repositoryRef: string,
    options: KubernetesListOptions = {},
    context?: APIReadContext,
  ): Promise<KubernetesListPage<Package>> {
    return mapListPage(
      await kubeListPage('Package', `${PACKAGE_REPO_LABEL}=${repositoryRef}`, options, requestReadContext(context)),
      pkgFromCR,
    )
  },

  async listPackages(repositoryRef: string): Promise<Package[]> {
    return (await kubeListAll(
      'Package',
      `${PACKAGE_REPO_LABEL}=${repositoryRef}`,
      requestReadContext(),
    )).map(pkgFromCR)
  },

  async listAllPackagesPage(
    options: KubernetesListOptions = {},
    context?: APIReadContext,
  ): Promise<KubernetesListPage<PackageRow>> {
    return mapListPage(
      await kubeListPage('Package', undefined, options, requestReadContext(context)),
      pkgRowFromCR,
    )
  },

  async listAllPackages(): Promise<PackageRow[]> {
    return (await kubeListAll('Package', undefined, requestReadContext())).map(pkgRowFromCR)
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
