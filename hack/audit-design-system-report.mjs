#!/usr/bin/env node
// Consolidate reviewed audit evidence. This is not the design authority or a CI baseline.
import { auditDirectory } from './audit-design-system-output.mjs'
import { readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
const dir = auditDirectory()
const read = name => JSON.parse(readFileSync(path.join(dir, name), 'utf8'))
const write = (name, data) => writeFileSync(path.join(dir, name), `${JSON.stringify(data, null, 2)}\n`)
let states = {}
try { states = read('remediation-status.json') } catch (error) { if (error.code !== 'ENOENT') throw error }
const schemaVersion = 1
const reviewFiles = ['canonical-review.json', 'pattern-review.json', 'style-review.json']
const refPaths = value => typeof value === 'string'
  ? [...value.matchAll(/(?:^|[\s`("'([])((?:docs|provider-sdk|providers|portal|hack|apis)\/[^\s`"')\],;]+)/g)].map(match => match[1].replace(/[.,:]$/, ''))
  : []
const refPath = value => refPaths(value)[0] || null
const lineParts = value => String(value).split(',').map(part => {
  const match = part.trim().match(/^(\d+)(?:-(\d+))?$/)
  return match ? { line: Number(match[1]), ...(match[2] ? { lineEnd: Number(match[2]) } : {}) } : null
}).filter(Boolean)
function location(raw) {
  if (typeof raw === 'string') {
    const match = raw.match(/^(.*):(\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*)\s*(?:—\s*(.*))?$/)
    if (match) return lineParts(match[2]).map(lines => ({ path: match[1], ...lines, description: match[3] || '' }))
    return [{ path: raw, line: null, description: '' }]
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
  let sourcePath = raw.path || raw.file || ''
  let lineValue = raw.line ?? raw.lines
  if (lineValue == null && typeof sourcePath === 'string') {
    const suffix = sourcePath.match(/^(.*):(\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*)$/)
    if (suffix) { sourcePath = suffix[1]; lineValue = suffix[2] }
  }
  const lines = lineValue == null ? [{ line: null }] : lineParts(lineValue)
  const detail = raw.detail || raw.description || ''
  const source = raw.source && raw.source !== detail ? `Source: ${raw.source}` : ''
  const description = [detail, source].filter(Boolean).join(' ')
  return lines.map(line => ({ path: sourcePath, ...line, ...(Number.isInteger(raw.lineEnd) && line.lineEnd === undefined ? { lineEnd: raw.lineEnd } : {}), description }))
}
function locations(raw) {
  return (Array.isArray(raw) ? raw : raw == null ? [] : [raw]).flatMap(location)
}
const strings = value => (Array.isArray(value) ? value : value == null ? [] : [value])
  .map(item => typeof item === 'string' ? item : JSON.stringify(item))
const items = value => (Array.isArray(value) ? value : value == null ? [] : [value])
function canonicalReferences(raw) {
  return items(raw).map(item => {
    const details = typeof item === 'object' && item !== null ? item : null
    const reference = typeof item === 'string' ? item : String(item.path || item.reference || item.contract || JSON.stringify(item))
    const paths = [...new Set([...refPaths(reference), ...(details ? Object.values(details).flatMap(v => refPaths(v)) : [])])]
    return { reference, paths, details }
  })
}
function automation(raw) {
  if (typeof raw === 'boolean') return { detectable: raw, notes: '' }
  if (typeof raw === 'string') return { detectable: /^yes\b/i.test(raw.trim()), notes: raw }
  if (raw && typeof raw === 'object') {
    const status = String(raw.status || raw.detectable || '').toLowerCase()
    return { detectable: status === 'yes' || status === 'true', notes: String(raw.method || raw.notes || raw.status || '') }
  }
  return { detectable: false, notes: '' }
}
function humanJudgment(raw) {
  if (typeof raw === 'boolean') return { required: raw, notes: '' }
  if (typeof raw === 'string') return { required: /^(?:yes|true|required)\b/i.test(raw.trim()), notes: raw }
  if (raw && typeof raw === 'object') {
    const value = raw.required ?? raw.status ?? raw.humanJudgmentRequired
    const required = typeof value === 'boolean' ? value : /^(?:yes|true|required)\b/i.test(String(value || '').trim())
    return { required, notes: String(raw.notes || raw.reason || raw.rationale || '') }
  }
  return { required: false, notes: '' }
}
function component(name, explicitPath = null) {
  const path = explicitPath || refPath(name)
  return { name: path && path === name ? path.split('/').at(-1) : String(name), path }
}
function routeRecords(raw, occurrences) {
  const explicit = items(raw).map(item => {
    const record = typeof item === 'object' && item !== null ? item : { route: String(item) }
    const route = String(record.route || record.template || record.name || record.path || '')
    const sourceReferences = locations(record.path)
    const components = strings(record.components).map(name => component(name))
    const sourcePath = record.path || route
    const provider = String(sourcePath).match(/providers\/([^/]+)\/portal\//)?.[1]
    const labelSurface = route.match(/^(Agents|App Studio|Code|Edges|Infrastructure|Kuery|Quickstart|Host|Portal)\b/i)?.[1]?.toLowerCase().replace('app studio', 'app-studio')
    const surface = record.surface || provider || (String(sourcePath).startsWith('portal/src/') ? 'host' : null) || labelSurface
    return { surface, route, sourceReferences, components, details: typeof item === 'object' ? item : null }
  })
  if (explicit.length) return explicit
  const groups = new Map()
  for (const occurrence of occurrences) {
    const provider = occurrence.path.match(/^providers\/([^/]+)\/portal\//)?.[1]
    const surface = provider || (occurrence.path.startsWith('portal/src/') ? 'host' : null)
    if (!surface) continue
    if (!groups.has(surface)) groups.set(surface, [])
    groups.get(surface).push(occurrence)
  }
  return [...groups].map(([surface, sourceReferences]) => ({
    surface,
    route: `${surface === 'host' ? 'Host' : surface} route/state families in route-review.json`,
    sourceReferences,
    components: [],
    details: null,
  }))
}
function relatedComponents(raw, routeRows, occurrences) {
  const result = []
  for (const item of strings(raw)) result.push(component(item))
  for (const route of routeRows) result.push(...route.components)
  for (const item of occurrences) if (item.path.endsWith('.vue')) result.push(component(item.path, item.path))
  const seen = new Set()
  return result.filter(item => { const key = `${item.name}\0${item.path || ''}`; if (seen.has(key)) return false; seen.add(key); return true })
}
function normalizeFinding(source, sourceReview) {
  const occurrences = locations(source.occurrences ?? source.evidence)
  const evidence = locations(source.evidence ?? source.occurrences)
  const relatedRoutes = routeRecords(source.relatedRoutes, occurrences)
  const auto = automation(source.automaticallyDetectable ?? source.automatable)
  const human = humanJudgment(source.humanJudgmentRequired ?? source.humanJudgment)
  return {
    schemaVersion,
    id: source.id,
    evidenceRevision: source.evidenceRevision || 'c78e87ebc (discovery baseline; resolved occurrences may no longer match current source)',
    category: source.category,
    severity: source.severity,
    confidence: source.confidence,
    description: source.description,
    evidence,
    occurrences,
    canonical: canonicalReferences(source.canonical),
    recommendedRemediation: source.recommendedRemediation || source.remediation,
    expectedBenefit: source.expectedBenefit || source.benefit,
    regressionRisks: strings(source.regressionRisks ?? source.risks),
    humanJudgmentRequired: human.required,
    humanJudgmentNotes: human.notes,
    automaticallyDetectable: auto.detectable,
    automationNotes: auto.notes,
    relatedRoutes,
    relatedComponents: relatedComponents(source.relatedComponents, relatedRoutes, occurrences),
    status: source.status,
    sourceReview,
    verification: strings(source.verification),
    ...(states[source.id] || {}),
  }
}
const findings = reviewFiles.flatMap(name => read(name).findings.map(f => normalizeFinding(f, name))).sort((a, b) => a.id.localeCompare(b.id))
function validateLocations(id, key, values) {
  if (!Array.isArray(values)) throw new Error(`${id}.${key} must be an array`)
  for (const item of values) {
    if (!item || typeof item !== 'object' || Array.isArray(item) || !(typeof item.path === 'string' && item.path.length > 0 || item.path === null) || !(Number.isInteger(item.line) || item.line === null) || typeof item.description !== 'string') throw new Error(`${id}.${key} has invalid location record`)
    if (item.lineEnd !== undefined && (!Number.isInteger(item.lineEnd) || item.line === null || item.lineEnd < item.line)) throw new Error(`${id}.${key} has invalid line range`)
  }
}
function validateFinding(f) {
  if (!/^(CAN|PAT|CSS)-([A-G])-?(\d{2,3})$/.test(f.id)) throw new Error(`Invalid stable finding id: ${f.id}`)
  if (!'ABCDEFG'.includes(f.category) || f.id.match(/^(?:CAN|PAT|CSS)-([A-G])/)[1] !== f.category) throw new Error(`${f.id} has invalid/mismatched category`)
  if (f.schemaVersion !== schemaVersion || typeof f.evidenceRevision !== 'string' || !f.evidenceRevision.trim()) throw new Error(`${f.id} has invalid schema/evidence revision`)
  for (const key of ['severity','confidence','description','recommendedRemediation','expectedBenefit','status','sourceReview']) if (typeof f[key] !== 'string' || !f[key].trim()) throw new Error(`${f.id}.${key} must be a non-empty string`)
  for (const key of ['humanJudgmentRequired','automaticallyDetectable']) if (typeof f[key] !== 'boolean') throw new Error(`${f.id}.${key} must be boolean`)
  for (const key of ['humanJudgmentNotes','automationNotes']) if (typeof f[key] !== 'string') throw new Error(`${f.id}.${key} must be a string`)
  for (const key of ['regressionRisks','verification']) if (!Array.isArray(f[key]) || f[key].some(value => typeof value !== 'string')) throw new Error(`${f.id}.${key} must be a string array`)
  for (const key of ['evidence','occurrences']) validateLocations(f.id, key, f[key])
  if (!Array.isArray(f.canonical) || f.canonical.some(ref => !ref || typeof ref.reference !== 'string' || !ref.reference.trim() || !Array.isArray(ref.paths) || ref.paths.some(p => typeof p !== 'string') || (ref.details !== null && (typeof ref.details !== 'object' || Array.isArray(ref.details))))) throw new Error(`${f.id}.canonical must be reference objects`)
  if (!Array.isArray(f.relatedRoutes)) throw new Error(`${f.id}.relatedRoutes must be an array`)
  for (const route of f.relatedRoutes) {
    if (!route || !(typeof route.surface === 'string' || route.surface === null) || typeof route.route !== 'string' || !route.route.trim() || !Array.isArray(route.components)) throw new Error(`${f.id}.relatedRoutes has invalid shape`)
    validateLocations(f.id, 'relatedRoutes.sourceReferences', route.sourceReferences)
    if (route.components.some(item => !item || typeof item.name !== 'string' || !(typeof item.path === 'string' || item.path === null))) throw new Error(`${f.id}.relatedRoutes.components has invalid shape`)
  }
  if (!Array.isArray(f.relatedComponents) || f.relatedComponents.some(item => !item || typeof item.name !== 'string' || !(typeof item.path === 'string' || item.path === null))) throw new Error(`${f.id}.relatedComponents has invalid shape`)
}
if (!findings.length) throw new Error('Missing reviewed findings')
for (const finding of findings) validateFinding(finding)
if (new Set(findings.map(f => f.id)).size !== findings.length) throw new Error('Duplicate finding identifier')
writeFileSync(path.join(dir, 'findings.jsonl'), findings.map(f => JSON.stringify(f)).join('\n') + '\n')
write('exceptions.json', {
  schemaVersion: 1,
  purpose: 'Audit dispositions only. This does not authorize source-conformance suppressions; those remain in hack/ui-conformance-exceptions.json.',
  legitimateVariations: findings.filter(f => f.category === 'F'),
  unresolvedJudgments: findings.filter(f => f.category === 'G' && !f.status.startsWith('resolved')),
  deferredViolations: findings.filter(f => !['F','G'].includes(f.category) && /defer|pending|blocked/.test(f.status)),
})
const analysis = read('analysis.json')
const clusters = Object.entries(analysis.normalizedDeclarationGroups).flatMap(([kind, groups]) => groups.map((g, index) => {
  const occurrences = g.occurrences
  const properties = g.propertySet || g.normalizedDeclarations.split(';').map(d => d.split(':')[0])
  let disposition, reason
  if (occurrences.every(o => o.path.startsWith('hack/'))) {
    disposition = 'fixture-only'
    reason = 'Synthetic preview framing; not a product duplicate.'
  } else if (occurrences.every(o => o.path.startsWith('provider-sdk/'))) {
    disposition = 'canonical-family'
    reason = 'Canonical implementations share declarations; consolidation requires an API/behavior benefit, not identical CSS alone.'
  } else if (properties.every(p => /^(?:display|position|align-.+|justify-.+|flex(?:-.+)?|grid(?:-.+)?|gap|(?:row|column)-gap|min-width|width|max-width|height|min-height|max-height|margin(?:-.+)?|padding(?:-.+)?|overflow(?:-.+)?|inset(?:-.+)?|top|left|right|bottom|box-sizing|z-index|list-style|text-align|white-space|overflow-wrap|word-break)$/.test(p))) {
    disposition = 'composition-signal'
    reason = 'Shared geometry alone does not establish equivalent UI purpose. Compare consumers in pattern-matrix.md; no new wrapper is justified by this signature.'
  } else if (occurrences.some(o => /(?:banner|inline-error|stale-banner|read-error|mutation-error|__deleting)/.test(o.selector))) {
    disposition = 'feedback-review'
    reason = 'Contextual notification family. See PAT-B02 and open-PR snapshot #1182; preserve local validation and announcement semantics.'
  } else if (occurrences.every(o => /(?:sr-only|__live|live-region|visually-hidden|discovery-status|__prompt)/.test(o.selector))) {
    disposition = 'accessibility-utility'
    reason = 'Repeated screen-reader-only mechanics are intentional; preserving live regions and labels matters more than reducing declarations. No universal canonical utility is exposed.'
  } else {
    disposition = 'candidate-not-proven'
    reason = kind === 'near' ? 'Same property names do not prove visual or semantic duplication. Candidate remains available for review alongside source-level pattern dispositions.' : 'Identical declarations are evidence of reuse opportunity, not a defect by themselves. No canonical replacement or behavioral equivalence proven for this cluster.'
  }
  return { id: `CLUSTER-${kind.toUpperCase()}-${String(index + 1).padStart(3,'0')}`, disposition, reason, ...g }
}))
write('style-cluster-review.json', {
  schemaVersion: 1,
  method: 'Deterministic triage of every normalized group; semantic review resides in findings and pattern matrix. Candidate-not-proven groups are expressly not dismissed or claimed manually verified.',
  summary: Object.fromEntries([...new Set(clusters.map(g => g.disposition))].sort().map(k => [k, clusters.filter(g => g.disposition === k).length])),
  clusters,
})
console.log(`Consolidated ${findings.length} findings and classified ${clusters.length} style-similarity groups.`)

const routeReview = read('route-review.json')
const browserCases = [...read('browser-cases.json'), ...read('browser-detail-cases.json')]
const observations = [...read('browser-observations.json').observations, ...read('browser-detail-observations.json').observations]
if (new Set(browserCases.map(item => item.id)).size !== browserCases.length) throw new Error('Duplicate browser case identifier')
const routeIDs = new Set(routeReview.routes.map(route => route.id))
const localFixtures = routeReview.currentSourceFixtures || {}
for (const id of Object.keys(localFixtures)) if (!routeIDs.has(id)) throw new Error(`Unknown fixture route ${id}`)
for (const item of browserCases) {
  if ((item.routeIDs || []).some(id => !routeIDs.has(id))) throw new Error(`Unknown explicit route family in browser case ${item.id}`)
}
const hostCases = {
  '/:orgID/:workspaceID': 'dashboard',
  '/:orgID/:workspaceID/providers': 'providers',
  '/:orgID/:workspaceID/settings/workspaces': 'workspace-settings',
  '/:orgID/:workspaceID/settings/organizations': 'organization-settings',
  '/:orgID/:workspaceID/mcp': 'mcp',
  '/:orgID/:workspaceID/create/mcp-server': 'mcp-create',
  '/organizations': 'organization-chooser',
  '/organizations/new': 'organization-create',
  '/:pathMatch(.*)*': 'not-found',
}
for (const name of ['providers','system-services','workspace-seeds','organizations','users','metering']) hostCases[`/bonkers/${name}`] = `admin-${name}`
const routeRows = routeReview.routes.map(route => {
  let caseIDs = []
  if (route.surface === 'host' && hostCases[route.template]) caseIDs = [hostCases[route.template]]
  else if (route.surface === 'host' && route.template.includes('/providers/:name/')) caseIDs = browserCases.filter(c => c.path.startsWith('/providers/')).map(c => c.id)
  else if (route.surface === 'kuery') caseIDs = ['kuery']
  else if (['agents','code','edges','infrastructure','quickstart','app-studio'].includes(route.surface)) {
    const suffix = route.template.replace(/\/:template\?$/, '').replace(/\/:type\?$/, '')
    const requested = `/providers/${route.surface}${suffix ? (suffix.startsWith('#') ? '' : '/') + suffix : ''}`
    caseIDs = browserCases.filter(c => c.path === requested).map(c => c.id)
  }
  caseIDs = [...new Set([...caseIDs, ...browserCases.filter(item => item.routeIDs?.includes(route.id)).map(item => item.id)])]
  const observed = observations.filter(o => caseIDs.includes(o.id))
  const anyObserved = observed.some(o => o.status === 'observed')
  let status = anyObserved ? 'observed-family-on-older-live-checkout' : observed.length ? 'blocked-by-live-state' : 'not-observed'
  let limitation = route.gap || (status === 'not-observed' ? 'No browser case for this specific alias, parameterized resource, authorization flow, or application state. Static dispatch is inventoried; no visual compliance claim.' : 'Only the recorded route/theme/default states were observed; parameter variations and mutation states are not implied.')
  if (route.template === '/login' && route.surface === 'host') { status = 'observed-as-survey-login-precondition'; limitation = 'Normal bearer-token form exercised to establish the live survey; no separate screenshot/state matrix.' }
  const fixtures = localFixtures[route.id] || []
  const liveStatus = status
  if (fixtures.length && ['not-observed', 'blocked-by-live-state'].includes(status)) {
    status = 'observed-in-current-source-fixture'
    limitation = 'Fixture evidence covers the recorded states. Live coverage is recorded separately.'
  }
  return { routeID: route.id, surface: route.surface, template: route.template, status, liveStatus, fixtures, browserCaseIDs: caseIDs, observationCount: observed.length, limitation }
})
write('route-coverage.json', {
  schemaVersion: 1,
  source: 'route-review.json (including currentSourceFixtures) + browser-cases.json + browser-detail-cases.json + browser-observations.json + browser-detail-observations.json',
  warning: 'Case-to-family mapping is coverage evidence only; old live checkout cfba73815 cannot verify changed local source. Multiple families may share a case and must not be summed as unique rendered routes.',
  summary: Object.fromEntries([...new Set(routeRows.map(r => r.status))].sort().map(k => [k, routeRows.filter(r => r.status === k).length])),
  routes: routeRows,
})
