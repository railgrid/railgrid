// GraphQL client for the infrastructure provider's portal.
//
// Every read and write goes through the hub's embedded GraphQL gateway at
// /graphql/<cluster> — the same workspace-scoped, caller-authenticated path the
// rest of the platform uses. The shell pushes farosContext.tenant (kcp cluster
// name, used as the /graphql path segment) and farosContext.token (bearer).
//
// The tenant-facing API surface is flat: Templates (the catalog) plus ONE
// Instance kind. Which product an Instance is rides in spec.template; its
// template-shaped input lives in spec.values (preserve-unknown, so the gateway
// serves it as a JSONString — full reads go through the raw `InstanceYaml`
// escape hatch parsed with js-yaml). Writes use `applyYaml` / `deleteInstance`.
// No kind discovery or introspection is needed anymore.

import { load as yamlLoad } from 'js-yaml'
import type { ErrorResponse, Instance, JSONSchema, Template, TemplateExposure, TemplateView } from './types'
import { columnsNeedInstanceData } from './view'

const GROUP = 'infrastructure.faros.sh'
const VERSION = 'v1alpha1'
// GraphQL field for the group (dots → underscores, per the gateway's sanitizer).
const GROUP_FIELD = 'infrastructure_faros_sh'

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

// setBasePath is a no-op: the gateway path is built from the cluster name, not
// the provider basePath. Kept so App.vue's watcher type-checks.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}
export function setToken(token?: string | null) {
  const next = token || null
  if (next !== bearerToken) {
    contextGeneration += 1
    // Template metadata is permissioned and may differ between callers even
    // when they share a tenant path. Never reuse one caller's cache after an
    // authentication-context change.
    cachedTemplates = null
    sampleValuesSupported = null
    viewSupported = null
    exposureSupported = null
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
    sampleValuesSupported = null
    viewSupported = null
    exposureSupported = null
  }
  clusterName = next
}

// ── GraphQL transport ───────────────────────────────────────────────────────
// graphqlQuery POSTs a query/mutation to /graphql/<cluster> and returns data,
// mapping gateway errors onto the {reason,message} contract the views branch on.
async function graphqlQuery<T>(query: string, variables: Record<string, unknown> = {}): Promise<T> {
  const expectedContext = requestContext()
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
  assertCurrentContext(expectedContext)
  if (!res.ok) {
    throw <ErrorResponse>{ reason: res.status === 404 ? 'NotFound' : 'HTTPError', message: text || res.statusText }
  }
  const body = (text ? JSON.parse(text) : {}) as { data?: T; errors?: { message: string }[] }
  if (body.errors && body.errors.length) {
    const message = body.errors.map(e => e.message).join('; ')
    let reason = 'GraphQLError'
    if (/not\s*found|notfound/i.test(message)) reason = 'NotFound'
    else if (/apibinding|no matches for kind|forbidden/i.test(message)) reason = 'APIBindingMissing'
    throw <ErrorResponse>{ reason, message }
  }
  assertCurrentContext(expectedContext)
  return (body.data ?? {}) as T
}

// applyCR applies a manifest (create-or-update) via the gateway's applyYaml and
// returns the resulting object (applyYaml serialises it as a JSON string).
async function applyCR(manifest: Record<string, unknown>): Promise<RawObject> {
  const data = await graphqlQuery<{ applyYaml?: unknown }>(
    'mutation($y: String!) { applyYaml(yaml: $y) }',
    { y: JSON.stringify(manifest) },
  )
  const raw = data.applyYaml
  return (typeof raw === 'string' ? JSON.parse(raw || '{}') : raw ?? {}) as RawObject
}

// Infra<V> shapes a gateway response nested under the infra group/version. The
// literal keys match GROUP_FIELD / VERSION, which are literal-typed consts, so
// `data[GROUP_FIELD]?.[VERSION]` indexes cleanly.
type Infra<V> = { infrastructure_faros_sh?: { v1alpha1?: V } }

interface RawObject {
  apiVersion?: string
  kind?: string
  metadata?: {
    uid?: string
    name?: string
    namespace?: string
    creationTimestamp?: string
    deletionTimestamp?: string
    generation?: number
    labels?: Record<string, string>
  }
  spec?: {
    template?: string
    // The gateway serves preserve-unknown fields as JSON strings; the Yaml
    // escape hatch yields the real object.
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
function templateFromGQL(name: string, spec: Record<string, unknown>, labels: Record<string, string> = {}): Template {
  const instanceCRD = (spec.instanceCRD ?? {}) as { kind?: string }
  // spec.schema is a preserve-unknown-fields field → the gateway returns it as a
  // JSON string (JSONString scalar); parse it back into the JSONSchema object.
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
  // sampleValues is a preserve-unknown-fields field too → same JSONString
  // treatment as schema: parse the string form, accept an object as-is.
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
  // view is a preserve-unknown-fields field → JSONString from the gateway;
  // same parse-the-string / accept-an-object treatment as schema/sampleValues.
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
// faros.sh/template label. spec.values may arrive as a JSON string (typed
// GraphQL read) or an object (Yaml escape hatch).
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

// sampleValues is a recent Template field. A gateway whose schema was built from
// an older CRD that predates it has no such field, and selecting an absent field
// is a hard GraphQL error that would break the whole catalog/provision query. So
// select it optimistically and, on that specific error, remember it's missing and
// retry without it (degrading to no form pre-fill). null = not yet probed.
let sampleValuesSupported: boolean | null = null
// view, like sampleValues, is a recent Template field. A gateway built from an
// older CRD has no such field and rejects the whole query if we select it, so we
// probe optimistically and drop it on that specific error. null = not yet probed.
let viewSupported: boolean | null = null
// exposure gets the same optimistic-probe treatment; without it the catalog
// pill degrades to the 'internal' default rather than the query failing.
let exposureSupported: boolean | null = null

// templateSpec is the shared Template spec selection set. sampleValues/view/
// exposure are omitted once we've learned the gateway doesn't expose them.
function templateSpec(): string {
  const sv = sampleValuesSupported === false ? '' : ' sampleValues'
  const vw = viewSupported === false ? '' : ' view'
  const ex = exposureSupported === false ? '' : ' exposure'
  return `displayName description category version iconURL instanceCRD { group version resource kind } schema${sv}${vw}${ex}`
}

// templateQuery runs a Template query built from templateSpec(), retrying when
// the gateway rejects an optional field (older CRD) by remembering it's missing
// and rebuilding the selection without it. Loops so a gateway missing both
// sampleValues and view degrades in two passes rather than failing.
async function templateQuery<T>(make: (spec: string) => string, variables: Record<string, unknown> = {}): Promise<T> {
  for (;;) {
    try {
      return await graphqlQuery<T>(make(templateSpec()), variables)
    } catch (e) {
      const msg = (e as { message?: string }).message ?? ''
      if (sampleValuesSupported !== false && msg.includes('sampleValues')) {
        sampleValuesSupported = false
        continue
      }
      if (viewSupported !== false && msg.includes('view')) {
        viewSupported = false
        continue
      }
      if (exposureSupported !== false && msg.includes('exposure')) {
        exposureSupported = false
        continue
      }
      throw e
    }
  }
}

async function fetchTemplates(): Promise<Template[]> {
  const data = await templateQuery<Infra<{ Templates?: { items?: Array<{ metadata: { name: string; labels?: Record<string, string> }; spec: Record<string, unknown> }> } }>>(
    spec => `{ ${GROUP_FIELD} { ${VERSION} { Templates { items { metadata { name labels } spec { ${spec} } } } } } }`,
  )
  const items = data[GROUP_FIELD]?.[VERSION]?.Templates?.items ?? []
  const templates = items.map(t => templateFromGQL(t.metadata.name, t.spec ?? {}, t.metadata.labels ?? {}))
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
function buildInstanceManifest(name: string, templateName: string, values: Record<string, unknown>) {
  return {
    apiVersion: GROUP + '/' + VERSION,
    kind: 'Instance',
    metadata: { name, labels: { 'faros.sh/template': templateName } },
    spec: { template: templateName, values },
  }
}

// fetchInstanceYaml reads the full Instance object (incl. the arbitrary
// values/status) via the gateway's raw InstanceYaml escape hatch.
async function fetchInstanceYaml(name: string): Promise<RawObject | null> {
  try {
    const data = await graphqlQuery<Infra<{ InstanceYaml?: string }>>(
      `query($n: String!) { ${GROUP_FIELD} { ${VERSION} { InstanceYaml(name: $n) } } }`,
      { n: name },
    )
    const text = data[GROUP_FIELD]?.[VERSION]?.InstanceYaml
    return text ? (yamlLoad(text) as RawObject) : null
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
    throw protocolError('GraphQL Instance list limit must be a positive safe integer; retry the read.')
  }
  if (continueToken !== undefined && typeof continueToken !== 'string') {
    throw protocolError('GraphQL Instance list continue must be a string; retry the read.')
  }
  return options
}

function optionalInstanceListString(
  collection: Record<string, unknown>,
  key: 'continue' | 'resourceVersion',
): string | undefined {
  if (!(key in collection) || collection[key] === undefined || collection[key] === null) return undefined
  if (typeof collection[key] !== 'string') {
    throw protocolError(`GraphQL returned an invalid Instance list ${key}; retry the read.`)
  }
  return collection[key] as string
}

function optionalRemainingItemCount(collection: Record<string, unknown>): number | undefined {
  if (!('remainingItemCount' in collection) || collection.remainingItemCount === undefined || collection.remainingItemCount === null) return undefined
  const value = collection.remainingItemCount
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw protocolError('GraphQL returned an invalid Instance list remainingItemCount; retry the read.')
  }
  return value
}

function optionalInstanceString(record: Record<string, unknown>, key: string, label: string): void {
  const value = record[key]
  if (value !== undefined && value !== null && typeof value !== 'string') {
    throw protocolError(`GraphQL returned malformed ${label}; retry the read.`)
  }
}

function validateInstanceListItem(value: unknown, index: number): RawObject {
  if (!isRecord(value) || !isRecord(value.metadata)) {
    throw protocolError(`GraphQL returned malformed Instances item ${index} metadata; retry the read.`)
  }
  const metadata = value.metadata
  if (typeof metadata.name !== 'string' || metadata.name.trim() === '') {
    throw protocolError(`GraphQL returned malformed Instances item ${index} metadata.name; retry the read.`)
  }
  for (const key of ['uid', 'namespace', 'creationTimestamp', 'deletionTimestamp']) {
    optionalInstanceString(metadata, key, `Instances item ${index} metadata.${key}`)
  }
  if (metadata.generation !== undefined && metadata.generation !== null &&
    (typeof metadata.generation !== 'number' || !Number.isSafeInteger(metadata.generation) || metadata.generation < 0)) {
    throw protocolError(`GraphQL returned malformed Instances item ${index} metadata.generation; retry the read.`)
  }
  if (metadata.labels !== undefined && metadata.labels !== null) {
    if (!isRecord(metadata.labels) || Object.values(metadata.labels).some(value => typeof value !== 'string')) {
      throw protocolError(`GraphQL returned malformed Instances item ${index} metadata.labels; retry the read.`)
    }
  }
  if (value.spec !== undefined && value.spec !== null && !isRecord(value.spec)) {
    throw protocolError(`GraphQL returned malformed Instances item ${index} spec; retry the read.`)
  }
  if (value.status !== undefined && value.status !== null) {
    if (!isRecord(value.status)) {
      throw protocolError(`GraphQL returned malformed Instances item ${index} status; retry the read.`)
    }
    const status = value.status
    if (status.observedGeneration !== undefined && status.observedGeneration !== null &&
      (typeof status.observedGeneration !== 'number' || !Number.isSafeInteger(status.observedGeneration) || status.observedGeneration < 0)) {
      throw protocolError(`GraphQL returned malformed Instances item ${index} status.observedGeneration; retry the read.`)
    }
    if (status.conditions !== undefined && status.conditions !== null) {
      if (!Array.isArray(status.conditions)) {
        throw protocolError(`GraphQL returned malformed Instances item ${index} status.conditions; retry the read.`)
      }
      status.conditions.forEach((condition, conditionIndex) => {
        if (!isRecord(condition) || typeof condition.type !== 'string' || condition.type.trim() === '' || typeof condition.status !== 'string') {
          throw protocolError(`GraphQL returned malformed Instances item ${index} status.conditions[${conditionIndex}]; retry the read.`)
        }
        for (const key of ['reason', 'message', 'lastTransitionTime']) {
          optionalInstanceString(condition, key, `Instances item ${index} status.conditions[${conditionIndex}].${key}`)
        }
      })
    }
  }
  return value as RawObject
}

const INSTANCE_LIST_SELECTION = 'items { metadata { uid name namespace creationTimestamp deletionTimestamp generation labels } spec { template } status { observedGeneration phase message conditions { type status reason message lastTransitionTime } } }'
const INSTANCE_IDENTITY_SELECTION = 'items { metadata { uid name } }'

async function fetchInstancePage(options: InstanceListOptions = {}): Promise<RawInstanceListPage> {
  const request = validateInstanceListOptions(options)
  const variables: Record<string, unknown> = {}
  if (request.limit !== undefined) variables.limit = request.limit
  if (request.continue !== undefined) variables.continue = request.continue
  let data: unknown
  try {
    data = await graphqlQuery<unknown>(
      `query($limit: Int, $continue: String) { ${GROUP_FIELD} { ${VERSION} { Instances(limit: $limit, continue: $continue) { ${INSTANCE_LIST_SELECTION} continue remainingItemCount resourceVersion } } } }`,
      variables,
    )
  } catch (e) {
    // A tenant that has not accepted the API binding sees the same stable
    // NotFound shape as the legacy list. Keep that read contract as empty.
    if ((e as ErrorResponse).reason === 'NotFound') return { items: [] }
    throw e
  }
  const group = isRecord(data) ? data[GROUP_FIELD] : undefined
  const version = isRecord(group) ? group[VERSION] : undefined
  const collection = isRecord(version) ? version.Instances : undefined
  if (!isRecord(collection) || !Array.isArray(collection.items)) {
    throw protocolError('GraphQL did not return a valid Instances list; retry the read.')
  }
  const items = collection.items.map(validateInstanceListItem)
  const nextToken = optionalInstanceListString(collection, 'continue')
  const remainingItemCount = optionalRemainingItemCount(collection)
  if (remainingItemCount !== undefined && remainingItemCount > 0 && !nextToken) {
    throw protocolError('GraphQL returned Instance remainingItemCount without a continuation token; retry the read.')
  }
  return {
    items,
    // Kubernetes uses an empty continuation token for a terminal page; keep
    // the typed page contract unambiguous by exposing terminal as undefined.
    continue: nextToken || undefined,
    remainingItemCount,
    resourceVersion: optionalInstanceListString(collection, 'resourceVersion'),
  }
}

function validateInstanceIdentityItem(value: unknown, index: number): { name: string; uid?: string } {
  if (!isRecord(value) || !isRecord(value.metadata)) {
    throw protocolError(`GraphQL returned malformed Instance identity ${index}; retry the read.`)
  }
  const metadata = value.metadata
  if (typeof metadata.name !== 'string' || metadata.name.trim() === '') {
    throw protocolError(`GraphQL returned malformed Instance identity ${index} name; retry the read.`)
  }
  optionalInstanceString(metadata, 'uid', `Instance identity ${index} uid`)
  return {
    name: metadata.name,
    ...(typeof metadata.uid === 'string' ? { uid: metadata.uid } : {}),
  }
}

async function fetchInstanceIdentityPage(options: InstanceListOptions = {}): Promise<RawInstanceIdentityPage> {
  const request = validateInstanceListOptions(options)
  const variables: Record<string, unknown> = {}
  if (request.limit !== undefined) variables.limit = request.limit
  if (request.continue !== undefined) variables.continue = request.continue
  let data: unknown
  try {
    data = await graphqlQuery<unknown>(
      `query($limit: Int, $continue: String) { ${GROUP_FIELD} { ${VERSION} { Instances(limit: $limit, continue: $continue) { ${INSTANCE_IDENTITY_SELECTION} continue remainingItemCount resourceVersion } } } }`,
      variables,
    )
  } catch (e) {
    if ((e as ErrorResponse).reason === 'NotFound') return { identities: [] }
    throw e
  }
  const group = isRecord(data) ? data[GROUP_FIELD] : undefined
  const version = isRecord(group) ? group[VERSION] : undefined
  const collection = isRecord(version) ? version.Instances : undefined
  if (!isRecord(collection) || !Array.isArray(collection.items)) {
    throw protocolError('GraphQL did not return a valid Instance identity list; retry the read.')
  }
  const identities = collection.items.map(validateInstanceIdentityItem)
  const nextToken = optionalInstanceListString(collection, 'continue')
  const remainingItemCount = optionalRemainingItemCount(collection)
  if (remainingItemCount !== undefined && remainingItemCount > 0 && !nextToken) {
    throw protocolError('GraphQL returned Instance identity remainingItemCount without a continuation token; retry the read.')
  }
  return {
    identities,
    continue: nextToken || undefined,
    remainingItemCount,
    resourceVersion: optionalInstanceListString(collection, 'resourceVersion'),
  }
}

async function enrichInstances(items: Instance[], templates: Template[]): Promise<void> {
  await Promise.all(
    items.map(async i => {
      const tmpl = templates.find(t => t.name === i.template)
      if (!tmpl || !columnsNeedInstanceData(tmpl.view)) return
      try {
        const full = await fetchInstanceYaml(i.name)
        if (!full) return
        const parsed = instanceFromObj(full)
        // InstanceYaml is a second read and can race a delete/recreate. Do
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
  const templates = await getTemplates()
  await enrichInstances(items, templates)
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
      throw protocolError('GraphQL returned a repeated Instance identity continuation token; retry the read.')
    }
    seenTokens.add(nextToken)
    continueToken = nextToken
  }

  throw protocolError(`GraphQL Instance identity list exceeded the ${MAX_INSTANCE_LIST_PAGES}-page safety limit; retry the read.`)
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
    const data = await templateQuery<Infra<{ Template?: { metadata: { name: string }; spec: Record<string, unknown> } }>>(
      spec => `query($n: String!) { ${GROUP_FIELD} { ${VERSION} { Template(name: $n) { metadata { name } spec { ${spec} } } } } }`,
      { n: name },
    )
    const t = data[GROUP_FIELD]?.[VERSION]?.Template
    if (!t) throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + name + ' not found' }
    assertCurrentContext(expectedContext)
    return { template: templateFromGQL(t.metadata.name, t.spec ?? {}) }
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
   * template lookups or InstanceYaml enrichment for off-page rows.
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
        throw protocolError('GraphQL returned a repeated Instance continuation token; retry the read.')
      }
      seenTokens.add(nextToken)
      continueToken = nextToken
    }

    throw protocolError(`GraphQL Instance list exceeded the ${MAX_INSTANCE_LIST_PAGES}-page safety limit; retry the read.`)
  },

  async getInstance(name: string): Promise<Instance> {
    const expectedContext = requestContext()
    const found = await fetchInstanceYaml(name)
    if (!found) throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'instance ' + name + ' not found' }
    assertCurrentContext(expectedContext)
    return instanceFromObj(found)
  },

  async deleteInstance(name: string): Promise<void> {
    const expectedContext = requestContext()
    await graphqlQuery(
      `mutation($n: String!) { ${GROUP_FIELD} { ${VERSION} { deleteInstance(name: $n) } } }`,
      { n: name },
    )
    assertCurrentContext(expectedContext)
  },
}
