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
        selector:
          matchLabels:
            railgrid.ai/owner: fixture
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
        selector:
          matchLabels:
            railgrid.ai/owner: fixture
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
  - defaultSelector:
      matchLabels:
        railgrid.ai/owner: fixture
    resource: secrets
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

// Turning the fixture's scoped secrets claim back into an unscoped one, in the
// generated APIExport. Spelled as a literal pair rather than a regex so the
// YAML the tests feed the parser stays valid.
const UNSCOPED_FROM = `  - defaultSelector:
      matchLabels:
        railgrid.ai/owner: fixture
    resource: secrets
`
const UNSCOPED_TO = `  - resource: secrets
`

// spec.dependencies[].composes[]: the manifest's OTHER way of declaring a
// claim. The generator turns each entry into an identityHash-less permission
// claim, so claims-parity must accept the resulting claim without a matching
// spec.apiExport.permissionClaims entry.
const DEPENDENCIES = `  dependencies:
    - name: edges
      composes:
        - group: edges.railgrid.ai
          resource: kubernetesclusters
          verbs: [get, list, watch]
    - name: rbac-lookalike
      composes:
        - group: rbac.authorization.k8s.io
          resource: clusterroles
          verbs: [get, create, delete]
`

const COMPOSED_CLAIM = `  - group: edges.railgrid.ai
    resource: kubernetesclusters
    verbs:
    - get
    - list
    - watch
`

/** The fixture manifest/chart with the compositions above declared. */
function withDependencies(text) {
  return text.replace('  actions:\n', `${DEPENDENCIES}  actions:\n`)
}

/** The generated export with the composed claim appended, as codegen writes it. */
function withComposedClaim(claim = COMPOSED_CLAIM) {
  return GENERATED_EXPORT.replace('  resources:', `${claim}  resources:`)
}

/** A repo whose manifest and chart declare the compositions. */
function composingRepo(overrides = {}) {
  return fixtureRepo({
    'providers/fixture/manifest.yaml': withDependencies(MANIFEST),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': withDependencies(CHART),
    [EXPORT_PATH]: withComposedClaim(),
    [CHART_EXPORT_PATH]: withComposedClaim(),
    ...overrides,
  })
}

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

test('a data-plane verb named after a standard Kubernetes verb is reported', () => {
  const withVerbs = (verbs) => MANIFEST.replace('  actions:\n', `  dataPlane:\n    verbs:\n${verbs}  actions:\n`)
  const chartWithVerbs = (verbs) => CHART.replace('  actions:\n', `  dataPlane:\n    verbs:\n${verbs}  actions:\n`)
  const bad = '      - resource: sessions\n        verb: update\n      - resource: sessions\n        verb: items\n'
  const result = fixtureRepo({
    'providers/fixture/manifest.yaml': withVerbs(bad),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': chartWithVerbs(bad),
  }).run()
  const reserved = result.violations.filter((item) => item.check === 'reserved-verb')
  assert.equal(reserved.length, 1)
  assert.match(reserved[0].message, /sessions\/update/)
  const good = bad.replace('verb: update', 'verb: edit')
  const clean = fixtureRepo({
    'providers/fixture/manifest.yaml': withVerbs(good),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': chartWithVerbs(good),
  }).run()
  assert.equal(clean.violations.filter((item) => item.check === 'reserved-verb').length, 0)
})

// Both declaration lists become kcp custom subresources named
// "<resource>/<verb>", so both are held to kcp's spec.resources[].name pattern.
const withVerbs = (text, verbs) => text.replace('  actions:\n', `  dataPlane:\n    verbs:\n${verbs}  actions:\n`)
const withAction = (text, id) => text.replace(
  '  - id: greet/v1\n',
  `  - id: ${id}\n    boundResource:\n      apiVersion: fixture.railgrid.ai/v1alpha1\n      kind: Widget\n      resource: widgets\n  - id: greet/v1\n`,
)

/** A repo whose manifest and chart both carry `mutate`, applied to each. */
function mutatedRepo(mutate) {
  return fixtureRepo({
    'providers/fixture/manifest.yaml': mutate(MANIFEST),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': mutate(CHART),
  })
}

function subresourceNames(result) {
  return result.violations.filter((item) => item.check === CHECKS.SUBRESOURCE_NAME)
}

test('a data-plane verb kcp cannot name as a custom subresource is reported', () => {
  const bad = '      - resource: sessions\n        verb: stage_upload\n      - resource: sessions\n        verb: edit\n'
  const reported = subresourceNames(mutatedRepo((text) => withVerbs(text, bad)).run())
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /sessions\/stage_upload/)
  assert.match(reported[0].message, /no underscores/)
  assert.equal(reported[0].path, 'providers/fixture/manifest.yaml')
  const good = bad.replace('verb: stage_upload', 'verb: stage-upload')
  assert.equal(subresourceNames(mutatedRepo((text) => withVerbs(text, good)).run()).length, 0)
})

test('a data-plane verb named status or scale is reported, whatever its resource', () => {
  const bad = '      - resource: instances\n        verb: status\n      - resource: instances\n        verb: scale\n'
  const reported = subresourceNames(mutatedRepo((text) => withVerbs(text, bad)).run())
  assert.equal(reported.length, 2)
  assert.ok(reported.some((item) => /instances\/status/.test(item.message)))
  assert.ok(reported.some((item) => /instances\/scale/.test(item.message)))
  for (const item of reported) assert.match(item.message, /belong to the object's shape/)
  const good = bad.replace('verb: status', 'verb: runtime-status').replace('verb: scale', 'verb: resize')
  assert.equal(subresourceNames(mutatedRepo((text) => withVerbs(text, good)).run()).length, 0)
})

test('an action id kcp cannot name as a custom subresource is reported', () => {
  const reported = subresourceNames(mutatedRepo((text) => withAction(text, 'mint_token/v1')).run())
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /spec\.actions\[0\] \(mint_token\/v1\)/)
  assert.match(reported[0].message, /widgets\/mint_token/)
  assert.equal(subresourceNames(mutatedRepo((text) => withAction(text, 'mint-token/v1')).run()).length, 0)
})

test('an action bound to no resource is left to the generator, not reported here', () => {
  // The fixture's own greet/v1 declares no boundResource: an incomplete
  // coordinate is not a NAME problem, and apiexportgen reports it as its own.
  assert.equal(subresourceNames(fixtureRepo().run()).length, 0)
})

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

test('a core-group secrets claim with no selector is reported', () => {
  // The whole point of X-4: an unscoped `secrets` claim is per RESOURCE, so it
  // reaches every Secret in every workspace that enables the provider. It
  // looks identical to a scoped one in review, which is why it is checked.
  const unscoped = MANIFEST.replace(
    '        tenantScoped: true\n        selector:\n          matchLabels:\n            railgrid.ai/owner: fixture\n',
    '        tenantScoped: true\n',
  )
  const result = fixtureRepo({
    'providers/fixture/manifest.yaml': unscoped,
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': CHART.replace(
      '        tenantScoped: true\n        selector:\n          matchLabels:\n            railgrid.ai/owner: fixture\n',
      '        tenantScoped: true\n',
    ),
    [EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
  }).run()
  const scoped = result.violations.filter((item) => item.check === CHECKS.CLAIM_SELECTOR)
  assert.equal(scoped.length, 1)
  assert.match(scoped[0].message, /core resource secrets with no selector\.matchLabels/)
  assert.match(scoped[0].message, /railgrid\.ai\/owner/)
  // Manifest and export agree that it is unscoped, so parity stays quiet: the
  // two checks answer different questions.
  assert.equal(result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY).length, 0)
})

test('a non-core claim needs no selector', () => {
  // clusterroles.rbac.authorization.k8s.io is claimed unscoped in the fixture
  // and must not be reported: the rule is about core-group credential
  // material, not about every claim.
  const result = fixtureRepo().run()
  assert.equal(result.violations.filter((item) => item.check === CHECKS.CLAIM_SELECTOR).length, 0)
})

test('a selector on only one of the manifest and the export is a parity violation', () => {
  const result = fixtureRepo({
    [EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
  }).run()
  const parity = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(parity.length, 2)
  assert.ok(parity.some((item) => /scoped railgrid\.ai\/owner=fixture but the generated APIExport does not/.test(item.message)))
  assert.ok(parity.some((item) => /claims secrets .* scoped \* but manifest\.yaml does not declare it/.test(item.message)))
})

test('a selector whose value drifts between the manifest and the export is reported', () => {
  const result = fixtureRepo({
    [EXPORT_PATH]: GENERATED_EXPORT.replace('railgrid.ai/owner: fixture', 'railgrid.ai/owner: somebody-else'),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace('railgrid.ai/owner: fixture', 'railgrid.ai/owner: somebody-else'),
  }).run()
  assert.equal(result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY).length, 2)
})

test('a claim only the manifest declares is reported', () => {
  const result = fixtureRepo({
    [EXPORT_PATH]: GENERATED_EXPORT.replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', ''),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', ''),
  }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /manifest\.yaml claims clusterroles\.rbac\.authorization\.k8s\.io \[create get\] scoped \* but the generated APIExport does not/)
})

test('a claim only the generated APIExport carries is reported', () => {
  const extra = GENERATED_EXPORT.replace('  resources:', '  - resource: configmaps\n    verbs:\n    - get\n  resources:')
  const result = fixtureRepo({ [EXPORT_PATH]: extra, [CHART_EXPORT_PATH]: extra }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY && /the generated APIExport claims configmaps \[get\] scoped \* but manifest\.yaml does not declare it/.test(item.message)))
})

test('differing verbs on the same resource are two reports, not a silent pass', () => {
  const narrowed = GENERATED_EXPORT.replace('    - get\n    - list\n    - watch\n', '    - get\n    - list\n')
  const result = fixtureRepo({ [EXPORT_PATH]: narrowed, [CHART_EXPORT_PATH]: narrowed }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 2)
  assert.ok(claims.some((item) => /manifest\.yaml claims secrets \[get list watch\]/.test(item.message)))
  assert.ok(claims.some((item) => /the generated APIExport claims secrets \[get list\]/.test(item.message)))
})

// --- composition-backed claims ---------------------------------------------
//
// A spec.dependencies[].composes[] entry IS a claim declaration: the generator
// emits one identityHash-less permission claim per entry the manifest's own
// spec.apiExport.permissionClaims does not already cover. claims-parity has to
// accept those, and only those.

test('a claim backed by a dependencies[].composes[] entry is accepted', () => {
  const result = composingRepo().run()
  assert.deepEqual(result.violations, [])
})

test('a composition that repeats a hand-written claim needs no second claim', () => {
  // The fixture composes clusterroles with a WIDER verb set than the manifest
  // claims. The generator keeps the hand-written claim and appends nothing, so
  // the export carries exactly one clusterroles claim with the manifest's verbs
  // -- and that must not read as a composition missing from the output.
  const result = composingRepo().run()
  assert.equal(result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY).length, 0)
})

test('a composition-backed claim whose verbs drifted from both sources is reported', () => {
  const drifted = withComposedClaim(`  - group: edges.railgrid.ai
    resource: kubernetesclusters
    verbs:
    - get
    - list
    - delete
`)
  const result = composingRepo({ [EXPORT_PATH]: drifted, [CHART_EXPORT_PATH]: drifted }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /the generated APIExport claims kubernetesclusters\.edges\.railgrid\.ai \[delete get list\] scoped \* but manifest\.yaml does not declare it/)
  assert.match(claims[0].message, /spec\.dependencies\[\]\.composes\[\]/)
})

test('a composition-backed claim narrowed by a selector is reported', () => {
  // A composition carries no selector, so a scoped claim is backed by nothing.
  const scoped = withComposedClaim(`  - defaultSelector:
      matchLabels:
        railgrid.ai/owner: fixture
    group: edges.railgrid.ai
    resource: kubernetesclusters
    verbs:
    - get
    - list
    - watch
`)
  const result = composingRepo({ [EXPORT_PATH]: scoped, [CHART_EXPORT_PATH]: scoped }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /scoped railgrid\.ai\/owner=fixture but manifest\.yaml does not declare it/)
})

test('a composition the generated APIExport does not claim is reported', () => {
  const result = composingRepo({ [EXPORT_PATH]: GENERATED_EXPORT, [CHART_EXPORT_PATH]: GENERATED_EXPORT }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /manifest\.yaml composes kubernetesclusters\.edges\.railgrid\.ai \[get list watch\] scoped \* but the generated APIExport claims no kubernetesclusters\.edges\.railgrid\.ai; run make codegen-fixture-provider/)
})

test('a hand-written claim missing from the output still fails when compositions exist', () => {
  // The composes[] source must not become a way for a declared permission
  // claim to go missing unnoticed.
  const dropped = withComposedClaim().replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', '')
  const result = composingRepo({ [EXPORT_PATH]: dropped, [CHART_EXPORT_PATH]: dropped }).run()
  const claims = result.violations.filter((item) => item.check === CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 2)
  assert.ok(claims.some((item) => /manifest\.yaml claims clusterroles\.rbac\.authorization\.k8s\.io \[create get\] scoped \* but the generated APIExport does not/.test(item.message)))
  assert.ok(claims.some((item) => /manifest\.yaml composes clusterroles\.rbac\.authorization\.k8s\.io \[create delete get\] scoped \* but the generated APIExport claims no clusterroles\.rbac\.authorization\.k8s\.io/.test(item.message)))
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

// An export that declares a custom subresource needs the review-API claim the
// proxied gate runs through the export virtual workspace.
const CUSTOM_SUBRESOURCE = `  - group: fixture.providers.railgrid.ai
    name: widgets/greet
    schema: v1alpha1.greet.fixture.providers.railgrid.ai
    storage:
      virtual:
        reference:
          apiGroup: dataplane.railgrid.ai
          kind: DataPlaneEndpointSlice
          name: fixture.providers.railgrid.ai
`
const ACCESS_CLAIM = `  - group: authorization.k8s.io
    resource: subjectaccessreviews
    verbs:
    - create
`
function accessClaims(result) {
  return result.violations.filter((item) => item.check === CHECKS.SUBRESOURCE_ACCESS_CLAIM)
}

test('a custom subresource without the subjectaccessreviews claim is reported', () => {
  const withSubresource = GENERATED_EXPORT + CUSTOM_SUBRESOURCE
  const reported = accessClaims(fixtureRepo({ [EXPORT_PATH]: withSubresource, [CHART_EXPORT_PATH]: withSubresource }).run())
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /widgets\/greet/)
  assert.match(reported[0].message, /authorization\.k8s\.io\/subjectaccessreviews/)
  assert.match(reported[0].message, /make codegen-fixture-provider/)

  const claimed = withSubresource.replace('  resources:', `${ACCESS_CLAIM}  resources:`)
  assert.equal(accessClaims(fixtureRepo({ [EXPORT_PATH]: claimed, [CHART_EXPORT_PATH]: claimed }).run()).length, 0)
})

test('an export with no custom subresource is not asked for the claim', () => {
  assert.equal(accessClaims(fixtureRepo().run()).length, 0)
})
