// GraphQL client for the infrastructure provider's portal.
//
// Every read and write goes through the hub's embedded GraphQL gateway at
// /graphql/<cluster> — the same workspace-scoped, caller-authenticated path the
// rest of the platform uses. The shell pushes kedgeContext.tenant (kcp cluster
// name, used as the /graphql path segment) and kedgeContext.token (bearer).
//
// Templates and per-template instance CRDs live in the infrastructure group, so
// they surface under the GraphQL field `infrastructure_kedge_faros_sh`. Instance
// kinds are declared per Template, so their list field (`<Plural>`) is discovered
// by introspection; reads of an instance's arbitrary spec use the gateway's raw
// `<Kind>Yaml` escape hatch (parsed with js-yaml), and writes use `applyYaml` /
// `delete<Kind>` mutations — no field schema needs to be known ahead of time.

import { load as yamlLoad } from 'js-yaml'
import type { ErrorResponse, Instance, JSONSchema, Template } from './types'

const GROUP = 'infrastructure.kedge.faros.sh'
const VERSION = 'v1alpha1'
// GraphQL field for the group (dots → underscores, per the gateway's sanitizer).
const GROUP_FIELD = 'infrastructure_kedge_faros_sh'

let bearerToken: string | null = null
let clusterName: string | null = null

// setBasePath is a no-op: the gateway path is built from the cluster name, not
// the provider basePath. Kept so App.vue's watcher type-checks.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}
export function setToken(token?: string | null) {
  bearerToken = token || null
}
export function setTenant(name?: string | null) {
  const next = name || null
  if (next !== clusterName) {
    // eslint-disable-next-line no-console
    console.debug('[infrastructure] tenant clusterName →', next)
  }
  clusterName = next
}

// ── GraphQL transport ───────────────────────────────────────────────────────
// graphqlQuery POSTs a query/mutation to /graphql/<cluster> and returns data,
// mapping gateway errors onto the {reason,message} contract the views branch on.
async function graphqlQuery<T>(query: string, variables: Record<string, unknown> = {}): Promise<T> {
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
    const message = body.errors.map(e => e.message).join('; ')
    let reason = 'GraphQLError'
    if (/not\s*found|notfound/i.test(message)) reason = 'NotFound'
    else if (/apibinding|no matches for kind|forbidden/i.test(message)) reason = 'APIBindingMissing'
    throw <ErrorResponse>{ reason, message }
  }
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
type Infra<V> = { infrastructure_kedge_faros_sh?: { v1alpha1?: V } }

interface RawObject {
  apiVersion?: string
  kind?: string
  metadata?: {
    name?: string
    namespace?: string
    creationTimestamp?: string
    labels?: Record<string, string>
  }
  spec?: Record<string, unknown>
  status?: {
    phase?: string
    message?: string
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }>
  }
}

// ── Mappers ─────────────────────────────────────────────────────────────────
function templateFromGQL(name: string, spec: Record<string, unknown>): Template {
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
  return {
    name,
    displayName: (spec.displayName as string) || name,
    description: (spec.description as string) ?? '',
    category: spec.category as string | undefined,
    cloud: spec.cloud as string | undefined,
    version: spec.version as string | undefined,
    iconURL: spec.iconURL as string | undefined,
    kind: instanceCRD.kind ?? '',
    inputsSchema,
    sampleValues,
  }
}

// instanceFromObj collapses a per-template CR (any object with metadata/spec/
// status) into the Instance shape the views read. The originating Template is
// taken from the kedge.faros.sh/template label, falling back to the kind.
function instanceFromObj(c: RawObject, templateByKind: Map<string, string>): Instance {
  const labels = c.metadata?.labels ?? {}
  const tmpl = labels['kedge.faros.sh/template'] || (c.kind ? templateByKind.get(c.kind) ?? c.kind : '')
  const conditions = (c.status?.conditions ?? []).map(cond => ({
    type: cond.type,
    status: cond.status,
    reason: cond.reason,
    message: cond.message,
    time: cond.lastTransitionTime,
  }))
  return {
    name: c.metadata?.name ?? '',
    namespace: c.metadata?.namespace ?? '',
    template: tmpl,
    phase: c.status?.phase || (conditions.find(x => x.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'),
    message: c.status?.message,
    conditions,
    values: c.spec,
    createdAt: c.metadata?.creationTimestamp ?? '',
  }
}

// ── Template + instance-field index ─────────────────────────────────────────
// Listing instances needs each kind's GraphQL list field (`<Plural>` =
// Pluralize(Kind), not derivable client-side), so we discover it by introspection
// once and cache it alongside the Templates (10s TTL — both change rarely).
interface InfraIndex {
  fetchedAt: number
  templates: Template[]
  templateByKind: Map<string, string>
  // kind → GraphQL list field name (only kinds whose CRD is actually established
  // in the workspace, so a Template with no bound CRD is naturally skipped).
  listFieldByKind: Map<string, string>
}
let cachedIndex: InfraIndex | null = null
const INDEX_TTL_MS = 10_000

// introspectVersionFields walks Query → infrastructure_kedge_faros_sh → v1alpha1
// in a single introspection query and returns its fields with (unwrapped) type
// names, so we can map each instance kind to its list field.
async function introspectVersionFields(): Promise<Array<{ name: string; typeName: string }>> {
  const q = `{ __type(name: "Query") { fields { name type { fields { name type { name fields { name type { name kind ofType { name kind } } } } } } } } }`
  const data = await graphqlQuery<{
    __type?: { fields?: Array<{ name: string; type?: { fields?: Array<{ name: string; type?: { fields?: Array<{ name: string; type?: { name?: string; ofType?: { name?: string } } }> } }> } }> }
  }>(q)
  const group = (data.__type?.fields ?? []).find(f => f.name === GROUP_FIELD)
  const version = (group?.type?.fields ?? []).find(f => f.name === VERSION)
  return (version?.type?.fields ?? []).map(f => ({
    name: f.name,
    typeName: f.type?.ofType?.name ?? f.type?.name ?? '',
  }))
}

// sampleValues is a recent Template field. A gateway whose schema was built from
// an older CRD that predates it has no such field, and selecting an absent field
// is a hard GraphQL error that would break the whole catalog/provision query. So
// select it optimistically and, on that specific error, remember it's missing and
// retry without it (degrading to no form pre-fill). null = not yet probed.
let sampleValuesSupported: boolean | null = null

// templateSpec is the shared Template spec selection set. sampleValues is omitted
// once we've learned the gateway doesn't expose it.
function templateSpec(): string {
  const sv = sampleValuesSupported === false ? '' : ' sampleValues'
  return `displayName description category version iconURL backend instanceCRD { group version resource kind } schema${sv}`
}

// templateQuery runs a Template query built from templateSpec(), retrying once
// without sampleValues if the gateway rejects that field (older CRD).
async function templateQuery<T>(make: (spec: string) => string, variables: Record<string, unknown> = {}): Promise<T> {
  try {
    return await graphqlQuery<T>(make(templateSpec()), variables)
  } catch (e) {
    const msg = (e as { message?: string }).message ?? ''
    if (sampleValuesSupported !== false && msg.includes('sampleValues')) {
      sampleValuesSupported = false
      return await graphqlQuery<T>(make(templateSpec()), variables)
    }
    throw e
  }
}

async function refreshIndex(): Promise<InfraIndex> {
  const [tmplData, versionFields] = await Promise.all([
    templateQuery<Infra<{ Templates?: { items?: Array<{ metadata: { name: string }; spec: Record<string, unknown> }> } }>>(
      spec => `{ ${GROUP_FIELD} { ${VERSION} { Templates { items { metadata { name } spec { ${spec} } } } } } }`,
    ),
    introspectVersionFields(),
  ])

  const items = tmplData[GROUP_FIELD]?.[VERSION]?.Templates?.items ?? []
  const templates = items.map(t => templateFromGQL(t.metadata.name, t.spec ?? {}))
  const templateByKind = new Map<string, string>()
  for (const t of templates) if (t.kind) templateByKind.set(t.kind, t.name)

  // Map kind → list field via the resource-type relationship: the list field's
  // type is `<resourceType>List`, the single field's type is `<resourceType>`.
  const listByResourceType = new Map<string, string>()
  const resourceTypeByKind = new Map<string, string>()
  for (const f of versionFields) {
    if (!f.typeName) continue
    if (f.typeName.endsWith('List')) listByResourceType.set(f.typeName.slice(0, -'List'.length), f.name)
    else if (f.typeName === 'String') continue // <Kind>Yaml fields
    else resourceTypeByKind.set(f.name, f.typeName) // single field: name === Kind
  }
  // Only map kinds that are actual Template instances — the schema also exposes
  // Template (and other) resources whose status has no phase/message, and which
  // must not be swept into the instance list.
  const instanceKinds = new Set(templates.map(t => t.kind).filter(Boolean))
  const listFieldByKind = new Map<string, string>()
  for (const [kind, resourceType] of resourceTypeByKind) {
    if (!instanceKinds.has(kind)) continue
    const lf = listByResourceType.get(resourceType)
    if (lf) listFieldByKind.set(kind, lf)
  }

  cachedIndex = { fetchedAt: Date.now(), templates, templateByKind, listFieldByKind }
  return cachedIndex
}

async function getIndex(force = false): Promise<InfraIndex> {
  if (!force && cachedIndex && Date.now() - cachedIndex.fetchedAt < INDEX_TTL_MS) return cachedIndex
  return refreshIndex()
}

// Build the wire manifest for a per-template instance CR. The kind/apiVersion
// come from the Template's instanceCRD (all instances live in the infra group);
// the input `values` go under .spec verbatim.
function buildInstanceManifest(kind: string, name: string, templateName: string, values: Record<string, unknown>) {
  return {
    apiVersion: GROUP + '/' + VERSION,
    kind,
    metadata: { name, labels: { 'kedge.faros.sh/template': templateName } },
    spec: values,
  }
}

export const api = {
  async listTemplates(filter: { category?: string; cloud?: string } = {}): Promise<{ items: Template[] }> {
    const idx = await refreshIndex()
    let items = idx.templates
    if (filter.category) items = items.filter(t => t.category === filter.category)
    if (filter.cloud) items = items.filter(t => t.cloud === filter.cloud)
    return { items }
  },

  async getTemplate(name: string): Promise<{ template: Template }> {
    const data = await templateQuery<Infra<{ Template?: { metadata: { name: string }; spec: Record<string, unknown> } }>>(
      spec => `query($n: String!) { ${GROUP_FIELD} { ${VERSION} { Template(name: $n) { metadata { name } spec { ${spec} } } } } }`,
      { n: name },
    )
    const t = data[GROUP_FIELD]?.[VERSION]?.Template
    if (!t) throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + name + ' not found' }
    return { template: templateFromGQL(t.metadata.name, t.spec ?? {}) }
  },

  async createInstance(body: {
    templateName: string
    templateVersion?: string
    name: string
    values: Record<string, unknown>
  }): Promise<Instance> {
    const idx = await getIndex()
    const tmpl = idx.templates.find(t => t.name === body.templateName)
    if (!tmpl || !tmpl.kind) {
      throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + body.templateName + ' not found' }
    }
    const manifest = buildInstanceManifest(tmpl.kind, body.name, body.templateName, body.values)
    const created = await applyCR(manifest)
    return instanceFromObj(created, idx.templateByKind)
  },

  async listInstances(): Promise<{ items: Instance[] }> {
    const idx = await getIndex()
    const kinds = [...idx.listFieldByKind.keys()]
    if (kinds.length === 0) return { items: [] }
    // One LIST per established kind, in parallel. metadata + status only — the
    // list view never needs the (arbitrary) spec, so we don't select it.
    const SEL = 'items { metadata { name namespace creationTimestamp labels } status { phase message conditions { type status reason message lastTransitionTime } } }'
    const lists = await Promise.all(
      kinds.map(async kind => {
        const field = idx.listFieldByKind.get(kind)!
        try {
          const data = await graphqlQuery<Infra<Record<string, { items?: RawObject[] }>>>(
            `{ ${GROUP_FIELD} { ${VERSION} { ${field} { ${SEL} } } } }`,
          )
          return data[GROUP_FIELD]?.[VERSION]?.[field]?.items ?? []
        } catch (e) {
          if ((e as ErrorResponse).reason === 'NotFound') return []
          throw e
        }
      }),
    )
    const items = lists.flat().map(c => instanceFromObj(c, idx.templateByKind))
    return { items }
  },

  async getInstance(name: string): Promise<Instance> {
    // The CR's kind isn't on the URL, so probe each established kind's raw
    // <Kind>Yaml in parallel and take the first hit. Yaml gives the full object
    // (incl. the arbitrary spec) without needing its schema.
    const idx = await getIndex()
    const kinds = [...idx.listFieldByKind.keys()]
    const probes = kinds.map(async kind => {
      try {
        const data = await graphqlQuery<Infra<Record<string, string>>>(
          `query($n: String!) { ${GROUP_FIELD} { ${VERSION} { ${kind}Yaml(name: $n) } } }`,
          { n: name },
        )
        const text = data[GROUP_FIELD]?.[VERSION]?.[kind + 'Yaml']
        return text ? (yamlLoad(text) as RawObject) : null
      } catch (e) {
        if ((e as ErrorResponse).reason === 'NotFound') return null
        throw e
      }
    })
    const found = (await Promise.all(probes)).find(Boolean)
    if (!found) throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'instance ' + name + ' not found' }
    return instanceFromObj(found, idx.templateByKind)
  },

  async deleteInstance(name: string): Promise<void> {
    const idx = await getIndex()
    // Resolve which kind the CR is, then delete<Kind>.
    const inst = await this.getInstance(name)
    const kind = idx.templates.find(t => t.name === inst.template)?.kind
    if (!kind) {
      throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'cannot resolve kind for ' + name }
    }
    await graphqlQuery(
      `mutation($n: String!) { ${GROUP_FIELD} { ${VERSION} { delete${kind}(name: $n) } } }`,
      { n: name },
    )
  },
}
