// View resolver — turns a template's View metadata (columns / detail groups)
// into rendered cells for an instance. Template authors reference an instance's
// data with dot-paths or ${…}-interpolated strings; this module is the single
// place those expressions are evaluated, so InstanceListPage, InstanceDetailPage
// and ViewValue all agree on the semantics.
//
// Three namespaces are exposed to expressions, matching the CR's own shape:
//   spec.*    → the instance's input values (Instance.values)
//   status.*  → controller-computed outputs (Instance.status)
//   meta.*    → name, namespace, phase, template, createdAt
// An unqualified first segment (e.g. "expose.fqdn") resolves against spec, so
// authors can write the common case without the "spec." prefix.

import type { FieldType, Instance, TemplateView, ViewExpr } from './types'

// A resolved cell: the display text plus, for links, the href to navigate to.
// `empty` lets callers hide rows/cells whose value didn't resolve.
export interface ResolvedValue {
  text: string
  type: FieldType
  href?: string
  empty: boolean
}

type Ctx = Record<string, unknown>

const NAMESPACES = new Set(['spec', 'status', 'meta'])

// buildContext assembles the {spec, status, meta} object expressions resolve
// against. Kept tiny and pure so it's cheap to call per row.
function buildContext(inst: Instance): Ctx {
  return {
    spec: inst.values ?? {},
    status: inst.status ?? {},
    meta: {
      name: inst.name,
      namespace: inst.namespace,
      phase: inst.phase,
      template: inst.template,
      createdAt: inst.createdAt,
    },
  }
}

// getPath walks a dot-path into the context. An unqualified first segment is
// treated as relative to spec. Returns undefined when any segment is missing.
function getPath(ctx: Ctx, path: string): unknown {
  const segments = path.split('.').filter(Boolean)
  if (segments.length === 0) return undefined
  if (!NAMESPACES.has(segments[0])) segments.unshift('spec')
  let cur: unknown = ctx
  for (const seg of segments) {
    if (cur == null || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}

// scalar renders a resolved value as a display string. Objects/arrays are
// JSON-stringified so a misaddressed path is visible rather than "[object]".
function scalar(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  try {
    return JSON.stringify(v)
  } catch {
    return String(v)
  }
}

// interpolate replaces ${path} tokens in a template string. A token that
// resolves to nothing becomes the empty string, so partial templates degrade
// gracefully instead of printing "undefined".
const TOKEN = /\$\{([^}]+)\}/g
function interpolate(tpl: string, ctx: Ctx): string {
  return tpl.replace(TOKEN, (_m, expr) => scalar(getPath(ctx, String(expr).trim())))
}

// evalExpr resolves a ViewExpr's primary value (path or value template).
function evalExpr(expr: ViewExpr, ctx: Ctx): string {
  if (expr.path) return scalar(getPath(ctx, expr.path))
  if (expr.value) return interpolate(expr.value, ctx)
  return ''
}

// withScheme prefixes a bare host/path with https:// so type=link targets are
// navigable even when the author stored just the FQDN.
function withScheme(href: string): string {
  if (!href) return href
  return /^[a-z][a-z0-9+.-]*:/i.test(href) ? href : 'https://' + href
}

// resolve evaluates one column/field against an instance into a render-ready
// cell. Pure — components call it directly in templates/computeds.
export function resolve(expr: ViewExpr, inst: Instance): ResolvedValue {
  const ctx = buildContext(inst)
  const text = evalExpr(expr, ctx)
  const type: FieldType = expr.type ?? 'text'
  let href: string | undefined
  if (type === 'link') {
    const raw = expr.href ? interpolate(expr.href, ctx) : text
    href = raw ? withScheme(raw) : undefined
  }
  return { text, type, href, empty: text.trim() === '' }
}

// exprRoots returns the namespaces (spec/status/meta) a ViewExpr references.
// Unqualified paths/tokens count as spec, matching getPath.
function exprRoots(expr: ViewExpr): string[] {
  const roots: string[] = []
  const add = (p: string) => {
    const seg = p.split('.').filter(Boolean)[0]
    roots.push(seg && NAMESPACES.has(seg) ? seg : 'spec')
  }
  if (expr.path) add(expr.path)
  for (const tpl of [expr.value, expr.href]) {
    if (!tpl) continue
    const re = /\$\{([^}]+)\}/g
    let m: RegExpExecArray | null
    while ((m = re.exec(tpl)) !== null) add(m[1].trim())
  }
  return roots
}

// columnsNeedInstanceData reports whether any of a view's list columns
// reference spec.* or status.* — i.e. data the lightweight instance LIST does
// not carry, so the caller must enrich those instances with the full object.
// Meta-only columns (name/phase/age) need no enrichment.
export function columnsNeedInstanceData(view?: TemplateView): boolean {
  if (!view?.columns?.length) return false
  return view.columns.some(c => exprRoots(c).some(r => r === 'spec' || r === 'status'))
}
