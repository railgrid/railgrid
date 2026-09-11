// Kubernetes REST client for the infrastructure provider's portal.
//
// Every read and write goes through the hub's kcp proxy at
// /clusters/<cluster>/apis/infrastructure.faros.sh/v1alpha1/... — the same
// workspace-scoped, caller-authenticated path kubectl would use. The shell
// pushes farosContext.tenant (kcp cluster name, used as the /clusters path
// segment) and farosContext.fetch (the host-owned transport that injects
// Authorization).
//
// The tenant-facing API surface is flat: Templates (the catalog) plus ONE
// Instance kind. Which product an Instance is rides in spec.template; its
// template-shaped input lives in spec.values. Objects come back whole, so
// reads are plain GET/LIST and writes are server-side apply / DELETE.

import type { ErrorResponse, Instance, InstanceChild, JSONSchema, Template, TemplateExposure, TemplateView } from './types'
import {
  createKubeClient,
  isKubeError,
  isKubeForbidden,
  isKubeNotFound,
  isKubeResourceUnavailable,
  type KubeClient,
  type KubeList,
  type KubeObject,
  type KubeResourceRef,
} from './portalkit/kube'
import { providerFetch, type ProviderFetch } from './portalkit/tenant'
import { columnsNeedInstanceData } from './view'

const GROUP = 'infrastructure.faros.sh'
const VERSION = 'v1alpha1'
const INSTANCES: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'instances' }
const TEMPLATES: KubeResourceRef = { group: GROUP, version: VERSION, resource: 'templates' }
// Field manager recorded on every server-side apply this portal performs.
const FIELD_MANAGER = 'provider-infrastructure'

let bearerToken: string | null = null
let clusterName: string | null = null
let contextGeneration = 0

class ContextChangedError extends Error {
  readonly reason = 'ContextChanged'

  constructor() {
    super('workspace context changed while the request was in flight')
    this.name = 'ContextChangedError'
  }
}
export function isContextChangedError(error: unknown): boolean {
  return error instanceof ContextChangedError || (error as { reason?: string } | null)?.reason === 'ContextChanged'
}

interface RequestContext {
  generation: number
  token: string | null
  tenant: string | null
}

function requestContext(): RequestContext {
  return { generation: contextGeneration, token: bearerToken, tenant: clusterName }
}

function assertCurrentContext(expected: RequestContext): void {
  if (expected.generation !== contextGeneration || expected.token !== bearerToken || expected.tenant !== clusterName) {
    throw new ContextChangedError()
  }
}

// setBasePath is a no-op: the REST path is built from the cluster name, not
// the provider basePath. Kept so App.vue's watcher type-checks.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}
// setHostFetch installs the host-owned transport from farosContext.fetch. The
// host injects Authorization itself; bearerToken then only fences in-flight
// requests, and providerFetch falls back to it on older hosts without fetch.
let hostFetch: ProviderFetch | null = null
export function setHostFetch(fetchImpl?: ProviderFetch | null) {
  hostFetch = fetchImpl ?? null
}
function hubFetch(): ProviderFetch {
  return providerFetch({ fetch: hostFetch, token: bearerToken })
}
export function setToken(token?: string | null) {
  const next = token || null
  if (next !== bearerToken) {
    contextGeneration += 1
    // Template metadata is permissioned and may differ between callers even
    // when they share a tenant path. Never reuse one caller's cache after an
    // authentication-context change.
    cachedTemplates = null
  }
  bearerToken = next
}
export function setTenant(name?: string | null) {
  const next = name || null
  if (next !== clusterName) {
    // eslint-disable-next-line no-console
    console.debug('[infrastructure] tenant clusterName →', next)
    contextGeneration += 1
    cachedTemplates = null
  }
  clusterName = next
}

// ── REST transport ──────────────────────────────────────────────────────────
// kube builds a client bound to the current tenant cluster. The request
// context is captured here, and the client's onResponse hook re-checks it
// after every body read, so a response that lands after the shell switched
// workspace (or re-authenticated) is rejected instead of committed.
function kube(): KubeClient {
  const expectedContext = requestContext()
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return createKubeClient({
    fetch: hubFetch(),
    cluster: clusterName,
    fieldManager: FIELD_MANAGER,
    onResponse: () => assertCurrentContext(expectedContext),
  })
}

// mapKubeError translates a KubeError onto the {reason,message} contract the
// views branch on. A 404 for a named object is NotFound; a 404 for the
// resource type (no APIBinding in the workspace yet) or a 403 is
// APIBindingMissing; a client-side wire failure (a 2xx the client could not
// make sense of) is ProtocolError; everything else is HTTPError. Non-kube
// errors — including the context fence — pass through untouched.
function mapKubeError(error: unknown): unknown {
  if (!isKubeError(error) || isContextChangedError(error)) return error
  if (error.status >= 200 && error.status < 300) {
    return protocolError(`${error.message}; retry the read.`)
  }
  let reason = 'HTTPError'
  if (isKubeResourceUnavailable(error) || isKubeForbidden(error)) reason = 'APIBindingMissing'
  else if (isKubeNotFound(error)) reason = 'NotFound'
  return <ErrorResponse>{ reason, message: error.message }
}

// withKube runs one client call and maps its failure onto ErrorResponse.
async function withKube<T>(run: (client: KubeClient) => Promise<T>): Promise<T> {
  const client = kube()
  try {
    return await run(client)
  } catch (e) {
    throw mapKubeError(e)
  }
}

// applyCR applies a manifest (create-or-update) with server-side apply and
// returns the resulting object.
function applyCR(manifest: RawObject): Promise<RawObject> {
  return withKube(client => client.apply<RawObject>(INSTANCES, manifest))
}

interface RawObject extends KubeObject {
  spec?: {
    template?: string
    values?: Record<string, unknown> | string
  }
  // status carries the well-known phase/message/conditions plus any
  // controller-computed output fields (url, fqdn, …) a template's View may
  // reference — hence the open-ended index signature.
  status?: {
    phase?: string
    message?: string
    observedGeneration?: number
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }>
    [k: string]: unknown
  }
}

/** Optional cursor controls accepted by the Instance list query. */
export interface InstanceListOptions {
  limit?: number
  continue?: string
}

/** A typed page returned by the cursor-based Instance list query. */
export interface InstanceListPage {
  items: Instance[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

interface RawInstanceListPage {
  items: RawObject[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

/** The identity-only page used to prove tombstone absence. */
export interface InstanceIdentityPage {
  identities: Array<{ name: string; uid?: string }>
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

interface RawInstanceIdentityPage {
  identities: Array<{ name: string; uid?: string }>
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

const INSTANCE_LIST_PAGE_SIZE = 100
const MAX_INSTANCE_LIST_PAGES = 100

// ── Mappers ─────────────────────────────────────────────────────────────────
// templateFromObject collapses a Template CR into the catalog shape. The
// preserve-unknown fields (schema, sampleValues, view) arrive as objects from
// the API server; the string form is still accepted so a serialised copy
// (e.g. from a cache) maps identically.
function templateFromObject(obj: KubeObject): Template {
  const name = obj.metadata.name
  const labels = obj.metadata.labels ?? {}
  const spec = isRecord(obj.spec) ? obj.spec : {}
  const instanceCRD = (spec.instanceCRD ?? {}) as { kind?: string }
  let inputsSchema: JSONSchema = { type: 'object', properties: {} }
  if (typeof spec.schema === 'string' && spec.schema) {
    try {
      inputsSchema = JSON.parse(spec.schema) as JSONSchema
    } catch {
      // leave the empty default
    }
  } else if (spec.schema && typeof spec.schema === 'object') {
    inputsSchema = spec.schema as JSONSchema
  }
  let sampleValues: Record<string, unknown> | undefined
  if (typeof spec.sampleValues === 'string' && spec.sampleValues) {
    try {
      sampleValues = JSON.parse(spec.sampleValues) as Record<string, unknown>
    } catch {
      // leave undefined — the form just starts empty
    }
  } else if (spec.sampleValues && typeof spec.sampleValues === 'object') {
    sampleValues = spec.sampleValues as Record<string, unknown>
  }
  let view: TemplateView | undefined
  if (typeof spec.view === 'string' && spec.view) {
    try {
      view = JSON.parse(spec.view) as TemplateView
    } catch {
      // leave undefined — instances fall back to the default rendering
    }
  } else if (spec.view && typeof spec.view === 'object') {
    view = spec.view as TemplateView
  }
  return {
    name,
    platformOwned: labels['faros.sh/platform-owned'] === 'true',
    displayName: (spec.displayName as string) || name,
    description: (spec.description as string) ?? '',
    category: spec.category as string | undefined,
    cloud: spec.cloud as string | undefined,
    exposure: spec.exposure as TemplateExposure | undefined,
    version: spec.version as string | undefined,
    iconURL: spec.iconURL as string | undefined,
    kind: instanceCRD.kind ?? '',
    inputsSchema,
    sampleValues,
    view,
  }
}

// instanceFromObj collapses an Instance CR into the shape the views read. The
// originating Template comes from spec.template, falling back to the
// faros.sh/template label. spec.values is an object on the wire; the string
// form is tolerated for serialised copies.
function instanceFromObj(c: RawObject): Instance {
  const labels = c.metadata?.labels ?? {}
  const tmpl = c.spec?.template || labels['faros.sh/template'] || ''
  let values: Record<string, unknown> | undefined
  if (typeof c.spec?.values === 'string') {
    try {
      values = JSON.parse(c.spec.values) as Record<string, unknown>
    } catch {
      // leave undefined — the detail page just shows no values
    }
  } else if (c.spec?.values && typeof c.spec.values === 'object') {
    values = c.spec.values
  }
  const conditions = (c.status?.conditions ?? []).map(cond => ({
    type: cond.type,
    status: cond.status,
    reason: cond.reason,
    message: cond.message,
    time: cond.lastTransitionTime,
  }))
  // status.children is controller-owned metadata that the detail page renders
  // in its bounded child-resource table. Promote only the established child
  // fields; keep the rest of status available to template-defined View fields
  // without leaking arbitrary child object data into the status namespace.
  const children: InstanceChild[] | undefined = Array.isArray(c.status?.children)
    ? c.status.children
      .filter(isRecord)
      .map(child => ({
        apiVersion: typeof child.apiVersion === 'string' ? child.apiVersion : '',
        kind: typeof child.kind === 'string' ? child.kind : '',
        name: typeof child.name === 'string' ? child.name : '',
        ...(typeof child.namespace === 'string' ? { namespace: child.namespace } : {}),
        ...(typeof child.phase === 'string' ? { phase: child.phase } : {}),
      }))
    : undefined
  // status outputs: everything under .status except the conditions/children
  // arrays (promoted to their own fields), so a View can reference status.*.
  let status: Record<string, unknown> | undefined
  if (c.status && typeof c.status === 'object') {
    const { conditions: _c, children: _ch, ...rest } = c.status as Record<string, unknown>
    void _c
    void _ch
    if (Object.keys(rest).length > 0) status = rest
  }
  return {
    uid: c.metadata?.uid,
    name: c.metadata?.name ?? '',
    namespace: c.metadata?.namespace ?? '',
    template: tmpl,
    deletionTimestamp: c.metadata?.deletionTimestamp,
    phase: c.metadata?.deletionTimestamp ? 'Deleting' : c.status?.phase || (conditions.find(x => x.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'),
    message: c.metadata?.deletionTimestamp ? 'Deletion is in progress while provisioned resources are being cleaned up.' : c.status?.message,
    conditions,
    children,
    values,
    status,
    createdAt: c.metadata?.creationTimestamp ?? '',
    generation: c.metadata?.generation,
    observedGeneration: c.status?.observedGeneration,
  }
}

// ── Template cache ──────────────────────────────────────────────────────────
// Templates change rarely; cache the list briefly so the instance pages don't
// re-fetch the catalog on every render.
interface TemplateCache {
  fetchedAt: number
  templates: Template[]
}
let cachedTemplates: TemplateCache | null = null
const CACHE_TTL_MS = 10_000

async function fetchTemplates(): Promise<Template[]> {
  const items = await withKube(client => client.listAll(TEMPLATES))
  const templates = items.map(templateFromObject)
  cachedTemplates = { fetchedAt: Date.now(), templates }
  return templates
}

async function getTemplates(force = false): Promise<Template[]> {
  if (!force && cachedTemplates && Date.now() - cachedTemplates.fetchedAt < CACHE_TTL_MS) {
    return cachedTemplates.templates
  }
  return fetchTemplates()
}

// Build the wire manifest for an Instance CR: the template name under
// spec.template, the form input under spec.values.
function buildInstanceManifest(name: string, templateName: string, values: Record<string, unknown>): RawObject {
  return {
    apiVersion: GROUP + '/' + VERSION,
    kind: 'Instance',
    metadata: { name, labels: { 'faros.sh/template': templateName } },
    spec: { template: templateName, values },
  }
}

// fetchInstanceObject reads the full Instance object (incl. the arbitrary
// values/status). A miss for the named object is null; a missing resource
// type or any other failure propagates as its mapped ErrorResponse.
async function fetchInstanceObject(name: string): Promise<RawObject | null> {
  try {
    return await withKube(client => client.get<RawObject>(INSTANCES, name))
  } catch (e) {
    if ((e as ErrorResponse).reason === 'NotFound') return null
    throw e
  }
}

function protocolError(message: string): ErrorResponse {
  return { reason: 'ProtocolError', message }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function validateInstanceListOptions(options: InstanceListOptions): InstanceListOptions {
  if (options === null || typeof options !== 'object' || Array.isArray(options)) {
    throw protocolError('Instance list options must be an object; retry the read.')
  }
  const { limit, continue: continueToken } = options
  if (limit !== undefined && (!Number.isSafeInteger(limit) || limit <= 0)) {
    throw protocolError('Instance list limit must be a positive safe integer; retry the read.')
  }
  if (continueToken !== undefined && typeof continueToken !== 'string') {
    throw protocolError('Instance list continue must be a string; retry the read.')
  }
  return options
}

function optionalInstanceString(record: Record<string, unknown>, key: string, label: string): void {
  const value = record[key]
  if (value !== undefined && value !== null && typeof value !== 'string') {
    throw protocolError(`the API returned malformed ${label}; retry the read.`)
  }
}

function validateInstanceListItem(value: unknown, index: number): RawObject {
  if (!isRecord(value) || !isRecord(value.metadata)) {
    throw protocolError(`the API returned malformed Instances item ${index} metadata; retry the read.`)
  }
  const metadata = value.metadata
  if (typeof metadata.name !== 'string' || metadata.name.trim() === '') {
    throw protocolError(`the API returned malformed Instances item ${index} metadata.name; retry the read.`)
  }
  for (const key of ['uid', 'namespace', 'creationTimestamp', 'deletionTimestamp']) {
    optionalInstanceString(metadata, key, `Instances item ${index} metadata.${key}`)
  }
  if (metadata.generation !== undefined && metadata.generation !== null &&
    (typeof metadata.generation !== 'number' || !Number.isSafeInteger(metadata.generation) || metadata.generation < 0)) {
    throw protocolError(`the API returned malformed Instances item ${index} metadata.generation; retry the read.`)
  }
  if (metadata.labels !== undefined && metadata.labels !== null) {
    if (!isRecord(metadata.labels) || Object.values(metadata.labels).some(value => typeof value !== 'string')) {
      throw protocolError(`the API returned malformed Instances item ${index} metadata.labels; retry the read.`)
    }
  }
  if (value.spec !== undefined && value.spec !== null && !isRecord(value.spec)) {
    throw protocolError(`the API returned malformed Instances item ${index} spec; retry the read.`)
  }
  if (value.status !== undefined && value.status !== null) {
    if (!isRecord(value.status)) {
      throw protocolError(`the API returned malformed Instances item ${index} status; retry the read.`)
    }
    const status = value.status
    if (status.observedGeneration !== undefined && status.observedGeneration !== null &&
      (typeof status.observedGeneration !== 'number' || !Number.isSafeInteger(status.observedGeneration) || status.observedGeneration < 0)) {
      throw protocolError(`the API returned malformed Instances item ${index} status.observedGeneration; retry the read.`)
    }
    if (status.conditions !== undefined && status.conditions !== null) {
      if (!Array.isArray(status.conditions)) {
        throw protocolError(`the API returned malformed Instances item ${index} status.conditions; retry the read.`)
      }
      status.conditions.forEach((condition, conditionIndex) => {
        if (!isRecord(condition) || typeof condition.type !== 'string' || condition.type.trim() === '' || typeof condition.status !== 'string') {
          throw protocolError(`the API returned malformed Instances item ${index} status.conditions[${conditionIndex}]; retry the read.`)
        }
        for (const key of ['reason', 'message', 'lastTransitionTime']) {
          optionalInstanceString(condition, key, `Instances item ${index} status.conditions[${conditionIndex}].${key}`)
        }
      })
    }
  }
  return value as RawObject
}

// listInstanceObjects fetches one cursor page of Instances. The kube client
// normalises the List envelope (continue is undefined on a terminal page,
// remainingItemCount without continue is a wire failure); the invariant is
// re-checked here so the typed page contract holds regardless of transport.
//
// A tenant that has not accepted the API binding sees the same stable
// empty read as the legacy list: a 404 on the collection can only mean the
// resource type is unavailable in this workspace, and the list pages keep
// that contract as empty rather than surfacing it as an error.
async function listInstanceObjects(options: InstanceListOptions): Promise<{ items: unknown[]; continue?: string; remainingItemCount?: number; resourceVersion?: string }> {
  const request = validateInstanceListOptions(options)
  const client = kube()
  let page: KubeList
  try {
    page = await client.list(INSTANCES, {
      ...(request.limit === undefined ? {} : { limit: request.limit }),
      ...(request.continue === undefined ? {} : { continue: request.continue }),
    })
  } catch (e) {
    if (isKubeNotFound(e)) return { items: [] }
    throw mapKubeError(e)
  }
  if (page.remainingItemCount !== undefined && page.remainingItemCount > 0 && !page.continue) {
    throw protocolError('the API returned Instance remainingItemCount without a continuation token; retry the read.')
  }
  return {
    items: page.items,
    // Kubernetes uses an empty continuation token for a terminal page; keep
    // the typed page contract unambiguous by exposing terminal as undefined.
    continue: page.continue || undefined,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

async function fetchInstancePage(options: InstanceListOptions = {}): Promise<RawInstanceListPage> {
  const page = await listInstanceObjects(options)
  return {
    items: page.items.map(validateInstanceListItem),
    continue: page.continue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

function validateInstanceIdentityItem(value: unknown, index: number): { name: string; uid?: string } {
  if (!isRecord(value) || !isRecord(value.metadata)) {
    throw protocolError(`the API returned malformed Instance identity ${index}; retry the read.`)
  }
  const metadata = value.metadata
  if (typeof metadata.name !== 'string' || metadata.name.trim() === '') {
    throw protocolError(`the API returned malformed Instance identity ${index} name; retry the read.`)
  }
  optionalInstanceString(metadata, 'uid', `Instance identity ${index} uid`)
  return {
    name: metadata.name,
    ...(typeof metadata.uid === 'string' ? { uid: metadata.uid } : {}),
  }
}

async function fetchInstanceIdentityPage(options: InstanceListOptions = {}): Promise<RawInstanceIdentityPage> {
  const page = await listInstanceObjects(options)
  return {
    identities: page.items.map(validateInstanceIdentityItem),
    continue: page.continue,
    remainingItemCount: page.remainingItemCount,
    resourceVersion: page.resourceVersion,
  }
}

async function enrichInstances(items: Instance[], templates: Template[]): Promise<void> {
  await Promise.all(
    items.map(async i => {
      const tmpl = templates.find(t => t.name === i.template)
      if (!tmpl || !columnsNeedInstanceData(tmpl.view)) return
      try {
        const full = await fetchInstanceObject(i.name)
        if (!full) return
        const parsed = instanceFromObj(full)
        // The detail GET is a second read and can race a delete/recreate. Do
        // not merge values from a same-name replacement into the listed UID.
        if (i.uid && parsed.uid && i.uid !== parsed.uid) return
        i.values = parsed.values
        i.status = parsed.status
      } catch {
        // Leave an unenriched baseline row rather than breaking the table.
      }
    }),
  )
}

async function listInstancesPage(options: InstanceListOptions = {}): Promise<InstanceListPage> {
  const expectedContext = requestContext()
  const raw = await fetchInstancePage(options)
  const items = raw.items.map(instanceFromObj)
  // Enrichment is deliberately page-local. This keeps a cursor page bounded
  // and prevents a page's template view from triggering reads for other pages.
  // An empty page needs no catalog: this also keeps the unbound-workspace
  // read (an empty list) from tripping over a missing Templates binding.
  if (items.length > 0) {
    const templates = await getTemplates()
    await enrichInstances(items, templates)
  }
  assertCurrentContext(expectedContext)
  return {
    items,
    continue: raw.continue,
    remainingItemCount: raw.remainingItemCount,
    resourceVersion: raw.resourceVersion,
  }
}

async function listInstanceIdentities(): Promise<Array<{ name: string; uid?: string }>> {
  const expectedContext = requestContext()
  const identities: Array<{ name: string; uid?: string }> = []
  const seenTokens = new Set<string>()
  let continueToken: string | undefined

  for (let pageNumber = 0; pageNumber < MAX_INSTANCE_LIST_PAGES; pageNumber += 1) {
    assertCurrentContext(expectedContext)
    const page = await fetchInstanceIdentityPage({
      limit: INSTANCE_LIST_PAGE_SIZE,
      ...(continueToken === undefined ? {} : { continue: continueToken }),
    })
    assertCurrentContext(expectedContext)
    identities.push(...page.identities)
    const nextToken = page.continue
    if (!nextToken) return identities
    if (seenTokens.has(nextToken)) {
      throw protocolError('the API returned a repeated Instance identity continuation token; retry the read.')
    }
    seenTokens.add(nextToken)
    continueToken = nextToken
  }

  throw protocolError(`Instance identity list exceeded the ${MAX_INSTANCE_LIST_PAGES}-page safety limit; retry the read.`)
}

export const api = {
  async listTemplates(filter: { category?: string; cloud?: string } = {}): Promise<{ items: Template[] }> {
    const expectedContext = requestContext()
    let items = await fetchTemplates()
    assertCurrentContext(expectedContext)
    items = items.filter(t => !t.platformOwned)
    if (filter.category) items = items.filter(t => t.category === filter.category)
    if (filter.cloud) items = items.filter(t => t.cloud === filter.cloud)
    return { items }
  },

  async getTemplate(name: string): Promise<{ template: Template }> {
    const expectedContext = requestContext()
    let t: KubeObject
    try {
      t = await withKube(client => client.get(TEMPLATES, name))
    } catch (e) {
      if ((e as ErrorResponse).reason === 'NotFound') {
        throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + name + ' not found' }
      }
      throw e
    }
    assertCurrentContext(expectedContext)
    return { template: templateFromObject(t) }
  },

  async createInstance(body: {
    templateName: string
    templateVersion?: string
    name: string
    values: Record<string, unknown>
  }): Promise<Instance> {
    const expectedContext = requestContext()
    const templates = await getTemplates()
    assertCurrentContext(expectedContext)
    if (!templates.some(t => t.name === body.templateName)) {
      throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + body.templateName + ' not found' }
    }
    const created = await applyCR(buildInstanceManifest(body.name, body.templateName, body.values))
    assertCurrentContext(expectedContext)
    return instanceFromObj(created)
  },

  async listInstancesPage(options: InstanceListOptions = {}): Promise<InstanceListPage> {
    return listInstancesPage(options)
  },

  /**
   * Walk only Instance metadata. This is intentionally separate from
   * listInstances(): callers proving deletion absence must not trigger
   * template lookups or per-object detail reads for off-page rows.
   */
  async listInstanceIdentities(): Promise<Array<{ name: string; uid?: string }>> {
    return listInstanceIdentities()
  },

  async listInstances(): Promise<{ items: Instance[]; identities: Array<{ name: string; uid?: string }> }> {
    const expectedContext = requestContext()
    const items: Instance[] = []
    const identities: Array<{ name: string; uid?: string }> = []
    const seenTokens = new Set<string>()
    let continueToken: string | undefined

    for (let pageNumber = 0; pageNumber < MAX_INSTANCE_LIST_PAGES; pageNumber += 1) {
      assertCurrentContext(expectedContext)
      const page = await listInstancesPage({
        limit: INSTANCE_LIST_PAGE_SIZE,
        ...(continueToken === undefined ? {} : { continue: continueToken }),
      })
      assertCurrentContext(expectedContext)
      items.push(...page.items)
      identities.push(...page.items.map(item => ({ name: item.name, uid: item.uid })))
      const nextToken = page.continue
      if (!nextToken) return { items, identities }
      if (seenTokens.has(nextToken)) {
        throw protocolError('the API returned a repeated Instance continuation token; retry the read.')
      }
      seenTokens.add(nextToken)
      continueToken = nextToken
    }

    throw protocolError(`Instance list exceeded the ${MAX_INSTANCE_LIST_PAGES}-page safety limit; retry the read.`)
  },

  async getInstance(name: string): Promise<Instance> {
    const expectedContext = requestContext()
    const found = await fetchInstanceObject(name)
    if (!found) throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'instance ' + name + ' not found' }
    assertCurrentContext(expectedContext)
    return instanceFromObj(found)
  },

  async deleteInstance(name: string): Promise<void> {
    const expectedContext = requestContext()
    await withKube(client => client.delete(INSTANCES, name))
    assertCurrentContext(expectedContext)
  },
}
