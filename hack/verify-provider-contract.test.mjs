import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { CHECKS, chartCatalogEntryYAML, parseYAML, verify } from './verify-provider-contract.mjs'

const MANIFEST = `# A comment block the parser has to skip.
---
apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  displayName: "Fixture"
  description: "A synthetic provider."
  vendor: "railgrid"
  version: "0.1.0"
  category: "Demo"
  iconURL: "/ui/providers/fixture/icon.svg"
  ui:
    url: "http://localhost:9099"
    indexPath: "/"
    children:
      - displayName: Widgets
        builtinRoute: widgets
  backend:
    url: "http://localhost:9099"
    healthPath: "/readyz"
  apiExport:
    name: "fixture.providers.railgrid.ai"
    permissionClaims:
      - resource: secrets
        verbs: [get, list, watch]
        tenantScoped: true
      - group: rbac.authorization.k8s.io
        resource: clusterroles
        verbs: ["get", "create"]
        tenantScoped: true
  actions:
  - id: greet/v1
    displayName: Greet
    description: A folded description that wraps onto
      a second line.
    limits:
      timeoutSeconds: 180
  selfHosting:
    supported: true
    chart:
      repository: "oci://ghcr.io/railgrid/charts"
      name: "railgrid-fixture-provider"
    namespace: "railgrid-provider-fixture"
    releaseName: "fixture"
`

const CHART = `{{- define "fixture.catalogEntry" -}}
apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  displayName: "Fixture"
  description: "A synthetic provider."
  vendor: "railgrid"
  version: {{ .Chart.AppVersion | quote }}
  category: "Demo"
  iconURL: "/ui/providers/fixture/icon.svg"
  ui:
    url: "http://{{ include "fixture.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.service.port }}"
    indexPath: "/"
    children:
      - displayName: Widgets
        builtinRoute: widgets
  backend:
    url: "http://{{ include "fixture.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.service.port }}"
    healthPath: "/readyz"
  apiExport:
    name: "fixture.providers.railgrid.ai"
    permissionClaims:
      - resource: secrets
        verbs: [get, list, watch]
        tenantScoped: true
      - group: rbac.authorization.k8s.io
        resource: clusterroles
        verbs: ["get", "create"]
        tenantScoped: true
  actions:
  - id: greet/v1
    displayName: Greet
    description: A folded description that wraps onto
      a second line.
    limits:
      timeoutSeconds: 180
{{- end -}}

{{- if .Values.catalogEntry.enabled -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "fixture.fullname" . }}-catalogentry
  labels:
    {{- include "fixture.labels" . | nindent 4 }}
data:
  catalogentry.yaml: |
{{ include "fixture.catalogEntry" . | indent 4 }}
      selfHosting:
        supported: true
        chart:
          repository: "oci://ghcr.io/railgrid/charts"
          name: "railgrid-fixture-provider"
          version: {{ .Chart.Version | quote }}
        namespace: "railgrid-provider-fixture"
        releaseName: "fixture"
        valuesDoc: |{{ .Files.Get "README.md" | nindent 10 }}
{{- end }}
`

// The generated APIExport: what codegen writes from the manifest above plus
// apigen's resources, and what `init` applies verbatim.
const GENERATED_EXPORT = `# Copyright 2026 The Railgrid Authors.
#
# GENERATED FILE -- DO NOT EDIT.
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: fixture.providers.railgrid.ai
spec:
  permissionClaims:
  - resource: secrets
    verbs:
    - get
    - list
    - watch
  - group: rbac.authorization.k8s.io
    resource: clusterroles
    verbs:
    - get
    - create
  resources:
  - group: fixture.providers.railgrid.ai
    name: widgets
    schema: v260919-abc1234.widgets.fixture.providers.railgrid.ai
    storage:
      crd: {}
`

const EXPORT_PATH = 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml'
const CHART_EXPORT_PATH = 'providers/fixture/deploy/chart/files/apiexport.yaml'

const MAIN_GO = `package main

// The doc comment may talk about /api/hello without tripping the scan.
func main() {
	mux.HandleFunc("/healthz", healthz)
	mux.Handle("/dataplane/clusters/", dataplane)
}
`

function fixtureRepo(overrides = {}) {
  const repoRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'railgrid-provider-contract-'))
  const files = {
    'providers/fixture/manifest.yaml': MANIFEST,
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': CHART,
    [EXPORT_PATH]: GENERATED_EXPORT,
    [CHART_EXPORT_PATH]: GENERATED_EXPORT,
    'providers/fixture/main.go': MAIN_GO,
    'providers/fixture/README.md': '# Fixture\n',
    'providers/fixture/deploy/chart/README.md': '# Fixture chart\n',
    ...overrides,
  }
  for (const [relative, content] of Object.entries(files)) {
    if (content === null) continue
    const absolute = path.join(repoRoot, relative)
    fs.mkdirSync(path.dirname(absolute), { recursive: true })
    fs.writeFileSync(absolute, content)
  }
  return {
    repoRoot,
    run(exceptions = { version: 1, exceptions: [] }) {
      return verify({ repoRoot, exceptions })
    },
  }
}

function checks(result) {
  return new Set(result.violations.map((item) => item.check))
}

test('a conformant provider reports nothing', () => {
  const result = fixtureRepo().run()
  assert.deepEqual(result.providers, ['fixture'])
  assert.deepEqual(result.violations, [])
})

test('only a directory with a manifest.yaml counts as a provider', () => {
  const fixture = fixtureRepo({ 'providers/portal-only/portal/src/element.ts': 'export {}\n' })
  assert.deepEqual(fixture.run().providers, ['fixture'])
})

test('the manifest/chart comparison skips Helm expressions and the per-release coordinates', () => {
  // version, both urls, selfHosting.chart.version and valuesDoc differ between
  // the two copies in the fixture above and must not be reported.
  const result = fixtureRepo().run()
  assert.equal(result.violations.filter((item) => item.check === CHECKS.MANIFEST_CHART_PARITY).length, 0)
  const chartSpec = parseYAML(chartCatalogEntryYAML(CHART)).find((document) => document.kind === 'CatalogEntry').spec
  assert.equal(chartSpec.selfHosting.releaseName, 'fixture')
  assert.equal(chartSpec.ui.children[0].builtinRoute, 'widgets')
  assert.equal(chartSpec.actions[0].limits.timeoutSeconds, 180)
})

test('a spec field that drifts from the chart is reported', () => {
  const result = fixtureRepo({
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': CHART.replace('healthPath: "/readyz"', 'healthPath: "/healthz"'),
  }).run()
  const parity = result.violations.filter((item) => item.check === CHECKS.MANIFEST_CHART_PARITY)
  assert.equal(parity.length, 1)
  assert.match(parity[0].message, /spec\.backend\.healthPath/)
  assert.match(parity[0].message, /\/readyz/)
})

test('a field present in only one copy is reported', () => {
  const result = fixtureRepo({
    'providers/fixture/manifest.yaml': MANIFEST.replace('  category: "Demo"\n', '  category: "Demo"\n  hubAccess:\n    - verb: list\n'),
  }).run()
  const parity = result.violations.filter((item) => item.check === CHECKS.MANIFEST_CHART_PARITY)
  assert.equal(parity.length, 1)
  assert.match(parity[0].message, /spec\.hubAccess: only manifest\.yaml sets it/)
})

test('a missing chart template is a parity violation, not a crash', () => {
  const result = fixtureRepo({ 'providers/fixture/deploy/chart/templates/catalogentry.yaml': null }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.MANIFEST_CHART_PARITY && /no chart CatalogEntry template/.test(item.message)))
})

test('a claim only the manifest declares is reported', () => {
  const result = fixtureRepo({
    [EXPORT_PATH]: GENERATED_EXPORT.replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', ''),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', ''),
  }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /manifest\.yaml claims clusterroles\.rbac\.authorization\.k8s\.io \[create get\] but the generated APIExport does not/)
})

test('a claim only the generated APIExport carries is reported', () => {
  const extra = GENERATED_EXPORT.replace('  resources:', '  - resource: configmaps\n    verbs:\n    - get\n  resources:')
  const result = fixtureRepo({ [EXPORT_PATH]: extra, [CHART_EXPORT_PATH]: extra }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY && /the generated APIExport claims configmaps \[get\] but manifest\.yaml does not declare it/.test(item.message)))
})

test('differing verbs on the same resource are two reports, not a silent pass', () => {
  const narrowed = GENERATED_EXPORT.replace('    - get\n    - list\n    - watch\n', '    - get\n    - list\n')
  const result = fixtureRepo({ [EXPORT_PATH]: narrowed, [CHART_EXPORT_PATH]: narrowed }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 2)
  assert.ok(claims.some((item) => /manifest\.yaml claims secrets \[get list watch\]/.test(item.message)))
  assert.ok(claims.some((item) => /the generated APIExport claims secrets \[get list\]/.test(item.message)))
})

// The export's name is the other half of the contract: the CatalogEntry points
// tenants at spec.apiExport.name, and that is the object init must create.
test('an APIExport named after something other than spec.apiExport.name is reported', () => {
  const renamed = GENERATED_EXPORT.replace('  name: fixture.providers.railgrid.ai', '  name: fixture.railgrid.ai')
  const result = fixtureRepo({ [EXPORT_PATH]: renamed, [CHART_EXPORT_PATH]: renamed }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY && /generated APIExport is named "fixture\.railgrid\.ai"/.test(item.message)))
})

test('a missing generated APIExport is reported, not a crash', () => {
  const result = fixtureRepo({ [EXPORT_PATH]: null, [CHART_EXPORT_PATH]: null }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /no generated APIExport at .*apiexport-fixture\.providers\.railgrid\.ai\.yaml; run make codegen-fixture-provider/)
  // export-copy stays quiet: there is nothing to copy yet.
  assert.equal(result.violations.filter((item) => item.check === CHECKS.EXPORT_COPY).length, 0)
})

test('the chart copy of the APIExport must be byte-identical', () => {
  const missing = fixtureRepo({ [CHART_EXPORT_PATH]: null }).run()
  assert.ok(missing.violations.some((item) => item.check === CHECKS.EXPORT_COPY && /the chart ships no apiexport\.yaml/.test(item.message)))

  const drifted = fixtureRepo({ [CHART_EXPORT_PATH]: `${GENERATED_EXPORT}# hand-edited\n` }).run()
  const copy = drifted.violations.filter((item) => item.check === CHECKS.EXPORT_COPY)
  assert.equal(copy.length, 1)
  assert.match(copy[0].message, /the chart copy is an output, run make codegen-fixture-provider/)
  assert.equal(copy[0].path, CHART_EXPORT_PATH)
  // The content is still equivalent, so claims parity has nothing to say.
  assert.equal(drifted.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY).length, 0)
})

test('both READMEs are required', () => {
  const result = fixtureRepo({ 'providers/fixture/README.md': null, 'providers/fixture/deploy/chart/README.md': null }).run()
  const readmes = result.violations.filter((item) => item.check === CHECKS.README_MISSING)
  assert.deepEqual(readmes.map((item) => item.message), ['deploy/chart/README.md is missing', 'README.md is missing'])
})

test('ad-hoc /api/ routes are reported with file:line, in every route dialect', () => {
  const result = fixtureRepo({
    'providers/fixture/main.go': `${MAIN_GO}\nfunc more() {\n\tmux.HandleFunc("/api/hello", hello)\n}\n`,
    'providers/fixture/server/routes.go': 'package server\n\nfunc routes() {\n\tmux.HandleFunc("GET /api/widgets", list)\n}\n',
    'providers/fixture/api/legacy.go': 'package api\n\nfunc legacy() {\n\trouter.PathPrefix("/api").Handler(h)\n}\n',
    'providers/fixture/api/legacy_test.go': 'package api\n\nfunc TestX() { call("/api/not-a-route") }\n',
    'providers/fixture/server/doc.go': 'package server\n\n// Historic: "/api/gone" was removed.\n/* and "/api/also-gone" */\n',
  }).run()
  const routes = result.violations.filter((item) => item.check === CHECKS.ADHOC_REST)
  assert.deepEqual(
    routes.map((item) => `${item.path}:${item.line}`),
    ['providers/fixture/api/legacy.go:4', 'providers/fixture/main.go:10', 'providers/fixture/server/routes.go:4'],
  )
  assert.ok(routes.some((item) => item.message.includes('"/api/widgets"')))
  assert.ok(routes.some((item) => item.message.includes('"/api"')))
})

test('an exception silences every violation of that provider and check', () => {
  const fixture = fixtureRepo({
    'providers/fixture/main.go': `${MAIN_GO}\nfunc more() {\n\tmux.HandleFunc("/api/a", a)\n\tmux.HandleFunc("/api/b", b)\n}\n`,
  })
  const result = fixture.run({
    version: 1,
    exceptions: [{ provider: 'fixture', check: CHECKS.ADHOC_REST, reason: 'tracked in provider-contract-remediation §1' }],
  })
  assert.deepEqual(result.violations, [])
  assert.equal(result.excused.length, 2)
  assert.equal(result.excused[0].reason, 'tracked in provider-contract-remediation §1')
})

test('an exception that no longer silences anything is itself a violation', () => {
  const result = fixtureRepo().run({
    version: 1,
    exceptions: [{ provider: 'fixture', check: CHECKS.ADHOC_REST, reason: 'tracked in provider-contract-remediation §1' }],
  })
  assert.deepEqual(checks(result), new Set([CHECKS.STALE_EXCEPTION]))
  assert.match(result.violations[0].message, /no longer silences anything; delete it/)
})

test('an exception for another provider does not silence this one', () => {
  const fixture = fixtureRepo({ 'providers/fixture/README.md': null })
  const result = fixture.run({
    version: 1,
    exceptions: [{ provider: 'other', check: CHECKS.README_MISSING, reason: 'tracked in provider-contract-remediation §1' }],
  })
  assert.deepEqual(checks(result), new Set([CHECKS.README_MISSING, CHECKS.STALE_EXCEPTION]))
})

test('the exception registry is validated', () => {
  const fixture = fixtureRepo()
  assert.throws(() => fixture.run({ version: 2, exceptions: [] }), /registry\.version must be 1/)
  assert.throws(() => fixture.run({ version: 1, exceptions: [{ provider: 'fixture', check: CHECKS.ADHOC_REST }] }), /reason must be a non-empty string/)
  assert.throws(() => fixture.run({ version: 1, exceptions: [{ provider: 'fixture', check: CHECKS.ADHOC_REST, reason: 'r', note: 'x' }] }), /unknown key "note"/)
  assert.throws(() => fixture.run({ version: 1, exceptions: [{ provider: 'fixture', check: CHECKS.STALE_EXCEPTION, reason: 'r' }] }), /is not an exceptable check/)
})

test('the YAML subset covers anchors, aliases, flow collections and block scalars', () => {
  const [document] = parseYAML(`spec:
  anchored: &base
    type: string
    minLength: 1
  reused: *base
  flow: [get, "list", 3]
  flowMap: {a: 1, b: "two"}
  literal: |
    line one
    line two
  folded: >-
    a folded
    sentence
  empty: []
  truthy: true
  nothing: null
`)
  assert.deepEqual(document.spec.reused, { type: 'string', minLength: 1 })
  assert.deepEqual(document.spec.flow, ['get', 'list', 3])
  assert.deepEqual(document.spec.flowMap, { a: 1, b: 'two' })
  assert.equal(document.spec.literal, 'line one\nline two\n')
  assert.equal(document.spec.folded, 'a folded sentence')
  assert.deepEqual(document.spec.empty, [])
  assert.equal(document.spec.truthy, true)
  assert.equal(document.spec.nothing, null)
})

test('the real tree passes with the checked-in exception registry', () => {
  const result = verify()
  assert.deepEqual(result.violations.map((item) => `${item.provider} ${item.check}: ${item.message}`), [])
  assert.ok(result.providers.length >= 7, `expected the in-tree providers, got ${result.providers.join(', ')}`)
})
