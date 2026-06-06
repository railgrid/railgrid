// kcp-native client for the infrastructure provider's portal.
//
// Talks to kcp directly through the hub's kcp REST proxy at
// /clusters/<tenant>/apis/infrastructure.kedge.faros.sh/v1alpha1/...
// — no more provider-side REST broker. The shell pushes:
//
//   kedgeContext.tenant  → kcp cluster name (auth.clusterName)
//   kedgeContext.token   → bearer for the kcp APIServer
//   kedgeContext.basePath → /ui/providers/infrastructure (kept only
//                           for legacy fallback callers that haven't
//                           been migrated; not used for kcp paths)
//
// The hub forwards anything under /clusters/<x>/{apis,api}/... to kcp
// after attaching the OIDC identity (see pkg/hub/server.go's kcpProxy
// mount point). The browser is authenticated with the same bearer
// that the kcp proxy validates.

import type { ErrorResponse, Instance, JSONSchema, Template } from './types'

const GROUP = 'infrastructure.kedge.faros.sh'
const VERSION = 'v1alpha1'
const APIS_PREFIX = `/apis/${GROUP}/${VERSION}`
const TEMPLATES_RESOURCE = 'templates'
// Per-template CRDs (Redis, Postgres, …) are cluster-scoped — see
// controller/template/controller.go where Scope is ClusterScoped.
// That means instance CRs live at <cluster>/apis/<g>/<v>/<plural>/<name>
// without a namespace segment.

let bearerToken: string | null = null
let clusterName: string | null = null
// setBasePath is kept as a no-op so App.vue's existing watcher still
// type-checks; the kcp path is constructed from clusterName, not the
// provider-name basePath, so the value is intentionally discarded.
export function setBasePath(_ctxBasePath?: string | null) {
  void _ctxBasePath
}

export function setToken(token?: string | null) {
  bearerToken = token || null
}

// setTenant accepts the kcp cluster name (kedgeContext.tenant from
// the shell, equal to auth.clusterName). Without it every call below
// throws an ErrorResponse with reason TenantMissing so views render
// their "no workspace selected" state instead of crashing.
export function setTenant(name?: string | null) {
  const next = name || null
  if (next !== clusterName) {
    // eslint-disable-next-line no-console
    console.debug('[infrastructure] tenant clusterName →', next)
  }
  clusterName = next
}

function clusterBase(): string {
  if (!clusterName) {
    throw <ErrorResponse>{ reason: 'TenantMissing', message: 'no workspace selected' }
  }
  return `/clusters/${clusterName}${APIS_PREFIX}`
}

interface KCPMetadata {
  name: string
  namespace?: string
  uid?: string
  resourceVersion?: string
  creationTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
}
interface KCPList<T> {
  items: T[]
}
interface KCPTemplate {
  metadata: KCPMetadata
  spec: {
    displayName?: string
    description?: string
    category?: string
    cloud?: string
    version?: string
    iconURL?: string
    backend?: string
    instanceCRD: { group: string; version: string; resource: string; kind: string }
    schema?: unknown // RawExtension — already a JSON object after .json() parse
    sampleValues?: Record<string, unknown>
  }
}
interface KCPInstance {
  apiVersion?: string
  kind?: string
  metadata: KCPMetadata
  spec?: Record<string, unknown>
  status?: {
    phase?: string
    message?: string
    conditions?: Array<{
      type: string
      status: string
      reason?: string
      message?: string
      lastTransitionTime?: string
    }>
  }
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
  // kcp returns Status objects on error; map them into the
  // {reason, message} contract the views are already coded against.
  if (!res.ok) {
    let reason = 'HTTPError'
    let message = text || res.statusText
    try {
      const parsed = JSON.parse(text) as { reason?: string; message?: string; status?: string; code?: number }
      if (parsed && (parsed.reason || parsed.message)) {
        reason = parsed.reason || reason
        message = parsed.message || message
      }
    } catch {
      // non-JSON body — keep raw text as the message
    }
    if (res.status === 404 && /^templates?\b/.test(path.split(APIS_PREFIX)[1] ?? '')) {
      reason = 'TemplateNotFound'
    } else if (res.status === 404) {
      reason = 'InstanceNotFound'
    } else if (res.status === 403 && /not\s+found.*APIBinding|no APIBinding/i.test(message)) {
      reason = 'APIBindingMissing'
    }
    throw <ErrorResponse>{ reason, message }
  }
  return (text ? JSON.parse(text) : null) as T
}

function templateFromKCP(t: KCPTemplate): Template {
  return {
    name: t.metadata.name,
    displayName: t.spec.displayName || t.metadata.name,
    description: t.spec.description ?? '',
    category: t.spec.category,
    cloud: t.spec.cloud,
    version: t.spec.version,
    iconURL: t.spec.iconURL,
    kind: t.spec.instanceCRD.kind,
    inputsSchema: (t.spec.schema as JSONSchema) ?? { type: 'object', properties: {} },
    sampleValues: t.spec.sampleValues,
  }
}

// instanceFromKCP collapses a per-template CR into the Instance shape
// the views read. The "template" field is the originating Template's
// name (carried via the kedge.faros.sh/template label set by the
// platform's CR-creation path; falls back to the CR's kind).
function instanceFromKCP(c: KCPInstance, templateByKind: Map<string, string>): Instance {
  const labels = c.metadata.labels ?? {}
  const tmpl = labels['kedge.faros.sh/template'] || (c.kind ? templateByKind.get(c.kind) ?? c.kind : '')
  const conditions = (c.status?.conditions ?? []).map(cond => ({
    type: cond.type,
    status: cond.status,
    reason: cond.reason,
    message: cond.message,
    time: cond.lastTransitionTime,
  }))
  return {
    name: c.metadata.name,
    namespace: c.metadata.namespace ?? '',
    template: tmpl,
    phase: c.status?.phase || (conditions.find(c => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'Pending'),
    message: c.status?.message,
    conditions,
    values: c.spec,
    createdAt: c.metadata.creationTimestamp ?? '',
  }
}

// Listing per-template CRs requires knowing each Template's plural.
// templateIndex memoizes the list and is bypassed when the caller
// asks for a fresh fetch. Templates change rarely; a 10s TTL is
// plenty for the auto-refresh InstanceListPage does.
interface TemplateIndex {
  fetchedAt: number
  templates: Template[]
  // Plural ('redis') ↔ Kind ('Redis') maps — both directions because
  // instance CRs only carry the Kind in their apiVersion+kind, but
  // list URLs need the plural.
  pluralByName: Map<string, string>
  kindByPlural: Map<string, string>
  templateByKind: Map<string, string>
}
let cachedIndex: TemplateIndex | null = null
const INDEX_TTL_MS = 10_000

async function refreshIndex(): Promise<TemplateIndex> {
  const list = await kcpFetch<KCPList<KCPTemplate>>('GET', clusterBase() + '/' + TEMPLATES_RESOURCE)
  const templates = (list.items ?? []).map(templateFromKCP)
  const pluralByName = new Map<string, string>()
  const kindByPlural = new Map<string, string>()
  const templateByKind = new Map<string, string>()
  for (const t of list.items ?? []) {
    pluralByName.set(t.metadata.name, t.spec.instanceCRD.resource)
    kindByPlural.set(t.spec.instanceCRD.resource, t.spec.instanceCRD.kind)
    templateByKind.set(t.spec.instanceCRD.kind, t.metadata.name)
  }
  cachedIndex = { fetchedAt: Date.now(), templates, pluralByName, kindByPlural, templateByKind }
  return cachedIndex
}

async function getIndex(force = false): Promise<TemplateIndex> {
  if (!force && cachedIndex && Date.now() - cachedIndex.fetchedAt < INDEX_TTL_MS) {
    return cachedIndex
  }
  return refreshIndex()
}

// Build the Resource Graph (the wire body) for a per-template CR.
// The instance kind/apiVersion come from the Template's spec.instanceCRD;
// the input `values` go under .spec verbatim.
function buildInstanceBody(tmpl: { kind: string; apiVersion: string }, name: string, values: Record<string, unknown>) {
  return {
    apiVersion: tmpl.apiVersion,
    kind: tmpl.kind,
    metadata: {
      name,
      labels: {
        'kedge.faros.sh/template': name && tmpl.kind ? tmpl.kind : '',
      },
    },
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
    const t = await kcpFetch<KCPTemplate>(
      'GET',
      clusterBase() + '/' + TEMPLATES_RESOURCE + '/' + encodeURIComponent(name),
    )
    return { template: templateFromKCP(t) }
  },

  async createInstance(body: {
    templateName: string
    templateVersion?: string
    name: string
    values: Record<string, unknown>
  }): Promise<Instance> {
    const idx = await getIndex()
    const tmplListItem = idx.templates.find(t => t.name === body.templateName)
    const plural = idx.pluralByName.get(body.templateName)
    if (!tmplListItem || !plural) {
      throw <ErrorResponse>{ reason: 'TemplateNotFound', message: 'template ' + body.templateName + ' not found' }
    }
    const apiVersion = GROUP + '/' + VERSION
    const cr = buildInstanceBody({ apiVersion, kind: tmplListItem.kind }, body.name, body.values)
    // Tag with the originating template name so listInstances can
    // attribute the CR back without a second lookup.
    cr.metadata.labels['kedge.faros.sh/template'] = body.templateName
    const created = await kcpFetch<KCPInstance>(
      'POST',
      clusterBase() + '/' + plural,
      cr,
    )
    return instanceFromKCP(created, idx.templateByKind)
  },

  async listInstances(): Promise<{ items: Instance[] }> {
    const idx = await getIndex()
    if (idx.templates.length === 0) return { items: [] }
    // One LIST per template. Parallel — kcp serves these from the
    // same workspace so concurrency is cheap. We tolerate per-kind
    // 404s (CRD not yet established) by treating them as empty.
    const lists = await Promise.all(
      idx.templates.map(async t => {
        const plural = idx.pluralByName.get(t.name)
        if (!plural) return [] as KCPInstance[]
        try {
          const r = await kcpFetch<KCPList<KCPInstance>>('GET', clusterBase() + '/' + plural)
          return r.items ?? []
        } catch (e) {
          const err = e as ErrorResponse
          if (err.reason === 'InstanceNotFound' || err.reason === 'TemplateNotFound') return []
          throw e
        }
      }),
    )
    const items = lists.flat().map(c => instanceFromKCP(c, idx.templateByKind))
    return { items }
  },

  async getInstance(name: string): Promise<Instance> {
    // The REST API didn't carry the template name on the URL, so the
    // only way to resolve a CR by bare name is to probe each Template's
    // plural. listInstances is too coarse; do parallel GETs and pick
    // the first 2xx.
    const idx = await getIndex()
    const probes = idx.templates.map(async t => {
      const plural = idx.pluralByName.get(t.name)
      if (!plural) return null
      try {
        return await kcpFetch<KCPInstance>(
          'GET',
          clusterBase() + '/' + plural + '/' + encodeURIComponent(name),
        )
      } catch (e) {
        const err = e as ErrorResponse
        if (err.reason === 'InstanceNotFound') return null
        throw e
      }
    })
    const found = (await Promise.all(probes)).find(Boolean)
    if (!found) {
      throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'instance ' + name + ' not found' }
    }
    return instanceFromKCP(found, idx.templateByKind)
  },

  async deleteInstance(name: string): Promise<void> {
    const idx = await getIndex()
    // Resolve which template the CR belongs to: cheap path is a label
    // lookup via getInstance, since that's a small batch of parallel
    // probes the browser is fine to pay for.
    const inst = await this.getInstance(name)
    const plural = idx.pluralByName.get(inst.template)
    if (!plural) {
      throw <ErrorResponse>{ reason: 'InstanceNotFound', message: 'cannot resolve plural for ' + name }
    }
    await kcpFetch<unknown>(
      'DELETE',
      clusterBase() + '/' + plural + '/' + encodeURIComponent(name),
    )
  },
}
