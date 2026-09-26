import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import { CHECKS, chartCatalogEntryYAML, parseYAML, verify } from './verify-provider-contract.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')

// The fixture in the shape apis/providers/v1alpha1 defines: one `export` with
// its verbs and actions hanging off the resource they are served on, one
// `requires` list keyed by group, and `serving` for where the hub reaches it.
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
  export:
    name: "fixture.providers.railgrid.ai"
    resources:
      - name: widgets
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: Widget
        actions:
          - name: greet
            version: v1
            displayName: Greet
            description: A folded description that wraps onto
              a second line.
            limits:
              timeoutSeconds: 180
  requires:
    - resources:
        - name: secrets
          verbs: [get, list, watch]
          selector:
            matchLabels:
              railgrid.ai/owner: fixture
    - group: rbac.authorization.k8s.io
      resources:
        - name: clusterroles
          verbs: ["get", "create"]
    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
  serving:
    ui:
      url: "http://localhost:9099"
      indexPath: "/"
      children:
        - displayName: Widgets
          builtinRoute: widgets
    backend:
      url: "http://localhost:9099"
      healthPath: "/readyz"
    selfHosting:
      supported: true
      chart:
        repository: "oci://ghcr.io/railgrid/charts"
        name: "railgrid-fixture-provider"
      namespace: "railgrid-provider-fixture"
      releaseName: "fixture"
`

// The chart copy: the same spec, reached through a `define`/`include ... indent`
// and carrying the per-release coordinates the comparison skips (the version,
// both urls, selfHosting.chart.version and valuesDoc).
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
  export:
    name: "fixture.providers.railgrid.ai"
    resources:
      - name: widgets
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: Widget
        actions:
          - name: greet
            version: v1
            displayName: Greet
            description: A folded description that wraps onto
              a second line.
            limits:
              timeoutSeconds: 180
  requires:
    - resources:
        - name: secrets
          verbs: [get, list, watch]
          selector:
            matchLabels:
              railgrid.ai/owner: fixture
    - group: rbac.authorization.k8s.io
      resources:
        - name: clusterroles
          verbs: ["get", "create"]
    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
  serving:
    ui:
      url: "http://{{ include "fixture.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.service.port }}"
      indexPath: "/"
      children:
        - displayName: Widgets
          builtinRoute: widgets
    backend:
      url: "http://{{ include "fixture.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.service.port }}"
      healthPath: "/readyz"
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
          valuesDoc: |{{ .Files.Get "README.md" | nindent 12 }}
{{- end }}
`

// The generated APIExport: what codegen writes from the manifest above plus
// apigen's resources, and what `init` applies verbatim. One claim per
// spec.requires resource entry, and one custom subresource per coordinate.
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
  - group: authorization.k8s.io
    resource: subjectaccessreviews
    verbs:
    - create
  resources:
  - group: fixture.railgrid.ai
    name: widgets
    schema: v260919-abc1234.widgets.fixture.railgrid.ai
    storage:
      crd: {}
  - group: fixture.railgrid.ai
    name: widgets/greet
    schema: v260919-abc1234.greet.fixture.railgrid.ai
    storage:
      virtual:
        reference:
          apiGroup: dataplane.railgrid.ai
          kind: DataPlaneEndpointSlice
          name: fixture.providers.railgrid.ai
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

// The requirement's selector, in the manifest and the chart alike.
const SELECTOR = `          selector:
            matchLabels:
              railgrid.ai/owner: fixture
`

// The review-API requirement every export that publishes a coordinate needs.
const ACCESS_REQUIREMENT = `    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
`
const ACCESS_CLAIM = `  - group: authorization.k8s.io
    resource: subjectaccessreviews
    verbs:
    - create
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

// --- the second provider ----------------------------------------------------
//
// The cross-reference checks (requires-group, requires-verb) hold a requirement
// against its OWNER's declaration, so they need a second provider in the tree.
// Its chart is derived from its manifest mechanically: the Helm rendering is
// already covered by the fixture above, and an exact copy keeps
// manifest-chart-parity quiet without a second hand-written template.

const PARTNER_MANIFEST = `apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: partner
spec:
  displayName: "Partner"
  export:
    name: "partner.providers.railgrid.ai"
    resources:
      - name: instances
        apiVersion: partner.railgrid.ai/v1alpha1
        kind: Instance
        verbs:
          - name: exec
            description: "Run a command in the instance."
            stream: true
        actions:
          - name: snapshot
            version: v1
            displayName: Snapshot
  requires:
    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
`

const PARTNER_EXPORT = `# GENERATED FILE -- DO NOT EDIT.
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: partner.providers.railgrid.ai
spec:
  permissionClaims:
  - group: authorization.k8s.io
    resource: subjectaccessreviews
    verbs:
    - create
  resources:
  - group: partner.railgrid.ai
    name: instances
    schema: v1.instances.partner.railgrid.ai
    storage:
      crd: {}
`

/** A chart template that embeds `manifestText` verbatim, with no Helm values. */
function chartFrom(manifestText) {
  const indented = manifestText
    .split('\n')
    .map((line) => (line === '' ? line : `    ${line}`))
    .join('\n')
  return `apiVersion: v1
kind: ConfigMap
metadata:
  name: partner-catalogentry
data:
  catalogentry.yaml: |
${indented}`
}

function partnerFiles(manifestText = PARTNER_MANIFEST, exportText = PARTNER_EXPORT) {
  return {
    'providers/partner/manifest.yaml': manifestText,
    'providers/partner/deploy/chart/templates/catalogentry.yaml': chartFrom(manifestText),
    'providers/partner/config/kcp/apiexport-partner.providers.railgrid.ai.yaml': exportText,
    'providers/partner/deploy/chart/files/apiexport.yaml': exportText,
    'providers/partner/README.md': '# Partner\n',
    'providers/partner/deploy/chart/README.md': '# Partner chart\n',
  }
}

/** The cross-provider requirement, as the fixture's manifest and chart write it. */
const PARTNER_REQUIREMENT = `    - provider: partner
      group: partner.railgrid.ai
      resources:
        - name: instances
          verbs: [get, list, watch]
        - name: instances/exec
`

/** The claims codegen emits for it: the kind's verbs, and every verb on the coordinate. */
const PARTNER_CLAIMS = `  - group: partner.railgrid.ai
    resource: instances
    verbs:
    - get
    - list
    - watch
  - group: partner.railgrid.ai
    resource: instances/exec
    verbs:
    - '*'
`

/** The fixture manifest/chart with `requirement` added to spec.requires. */
function withRequirement(text, requirement = PARTNER_REQUIREMENT) {
  return text.replace('  requires:\n', `  requires:\n${requirement}`)
}

/** The generated export with `claims` appended, as codegen writes them. */
function withClaims(claims = PARTNER_CLAIMS, base = GENERATED_EXPORT) {
  return base.replace('  resources:\n', `${claims}  resources:\n`)
}

/**
 * A two-provider repo: the fixture requires a kind AND a verb coordinate of
 * `partner`, and partner declares both.
 */
function pairedRepo(overrides = {}, { requirement = PARTNER_REQUIREMENT, claims = PARTNER_CLAIMS } = {}) {
  return fixtureRepo({
    ...partnerFiles(),
    'providers/fixture/manifest.yaml': withRequirement(MANIFEST, requirement),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': withRequirement(CHART, requirement),
    [EXPORT_PATH]: withClaims(claims),
    [CHART_EXPORT_PATH]: withClaims(claims),
    ...overrides,
  })
}

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

/** A repo whose manifest and chart both carry `mutate`, applied to each. */
function mutatedRepo(mutate, overrides = {}) {
  return fixtureRepo({
    'providers/fixture/manifest.yaml': mutate(MANIFEST),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': mutate(CHART),
    ...overrides,
  })
}

function checks(result) {
  return new Set(result.violations.map((item) => item.check))
}

function only(result, check) {
  return result.violations.filter((item) => item.check === check)
}

// An extra export resource, in the group the generated export serves. Both
// declaration lists hang off a resource now, so this is how a test adds one.
function resourceBlock(name, kind, body) {
  return `      - name: ${name}
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: ${kind}
${body}`
}

const withResource = (text, block) => text.replace('      - name: widgets\n', `${block}      - name: widgets\n`)
const sessions = (verbs) => resourceBlock('sessions', 'Session', `        verbs:\n${verbs}`)
const verb = (name) => `          - name: ${name}\n`
const withAction = (text, name, version = 'v1') => text.replace(
  '          - name: greet\n',
  `          - name: ${name}\n            version: ${version}\n            displayName: Extra\n          - name: greet\n`,
)

// ---------------------------------------------------------------------------
// reserved-verb
// ---------------------------------------------------------------------------

test('an export verb named after a standard Kubernetes verb is reported', () => {
  const bad = (text) => withResource(text, sessions(verb('update') + verb('items')))
  const reported = only(mutatedRepo(bad).run(), CHECKS.RESERVED_VERB)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /sessions\/update/)
  assert.match(reported[0].message, /spec\.export\.resources\[0\]\.verbs\[0\]/)
  const good = (text) => withResource(text, sessions(verb('edit') + verb('items')))
  assert.equal(only(mutatedRepo(good).run(), CHECKS.RESERVED_VERB).length, 0)
})

test('an ACTION named after a standard Kubernetes verb is reported too', () => {
  // Verbs and actions share one coordinate namespace, so the rule is the same
  // for both: `widgets/delete` reads as the object's own delete either way.
  const reported = only(mutatedRepo((text) => withAction(text, 'delete')).run(), CHECKS.RESERVED_VERB)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /widgets\/delete/)
  assert.match(reported[0].message, /actions\[0\] \(delete\/v1\)/)
})

// ---------------------------------------------------------------------------
// subresource-name
// ---------------------------------------------------------------------------

test('an export verb kcp cannot name as a custom subresource is reported', () => {
  const bad = (text) => withResource(text, sessions(verb('stage_upload') + verb('edit')))
  const reported = only(mutatedRepo(bad).run(), CHECKS.SUBRESOURCE_NAME)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /sessions\/stage_upload/)
  assert.match(reported[0].message, /no underscores/)
  assert.equal(reported[0].path, 'providers/fixture/manifest.yaml')
  const good = (text) => withResource(text, sessions(verb('stage-upload') + verb('edit')))
  assert.equal(only(mutatedRepo(good).run(), CHECKS.SUBRESOURCE_NAME).length, 0)
})

test('a verb named status or scale is reported, whatever its resource', () => {
  const bad = (text) => withResource(text, sessions(verb('status') + verb('scale')))
  const reported = only(mutatedRepo(bad).run(), CHECKS.SUBRESOURCE_NAME)
  assert.equal(reported.length, 2)
  assert.ok(reported.some((item) => /sessions\/status/.test(item.message)))
  assert.ok(reported.some((item) => /sessions\/scale/.test(item.message)))
  for (const item of reported) assert.match(item.message, /belong to the object's shape/)
  const good = (text) => withResource(text, sessions(verb('runtime-status') + verb('resize')))
  assert.equal(only(mutatedRepo(good).run(), CHECKS.SUBRESOURCE_NAME).length, 0)
})

test('an action name kcp cannot name as a custom subresource is reported', () => {
  const reported = only(mutatedRepo((text) => withAction(text, 'mint_token')).run(), CHECKS.SUBRESOURCE_NAME)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /actions\[0\] \(mint_token\/v1\)/)
  assert.match(reported[0].message, /"widgets\/mint_token"/)
  assert.equal(only(mutatedRepo((text) => withAction(text, 'mint-token')).run(), CHECKS.SUBRESOURCE_NAME).length, 0)
})

test('an action contributes its name only, never name/version', () => {
  // The version is in no path: the serving provider restores it from its own
  // declaration. `mint-token` v7 publishes `widgets/mint-token`, which kcp
  // accepts -- `widgets/mint-token/v7` would not.
  const reported = only(mutatedRepo((text) => withAction(text, 'mint_token', 'v7')).run(), CHECKS.SUBRESOURCE_NAME)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /"widgets\/mint_token"/)
  assert.ok(!/mint_token\/v7"/.test(reported[0].message))
  assert.equal(only(mutatedRepo((text) => withAction(text, 'mint-token', 'v7')).run(), CHECKS.SUBRESOURCE_NAME).length, 0)
})

test('a resource with no verbs and no actions publishes nothing to name', () => {
  const bare = (text) => withResource(text, resourceBlock('sessions', 'Session', ''))
  assert.equal(only(mutatedRepo(bare).run(), CHECKS.SUBRESOURCE_NAME).length, 0)
})

// ---------------------------------------------------------------------------
// the conformant baseline
// ---------------------------------------------------------------------------

test('a conformant provider reports nothing', () => {
  const result = fixtureRepo().run()
  assert.deepEqual(result.providers, ['fixture'])
  assert.deepEqual(result.violations, [])
})

test('only a directory with a manifest.yaml counts as a provider', () => {
  const fixture = fixtureRepo({ 'providers/portal-only/portal/src/element.ts': 'export {}\n' })
  assert.deepEqual(fixture.run().providers, ['fixture'])
})

// ---------------------------------------------------------------------------
// manifest-chart-parity
// ---------------------------------------------------------------------------

test('the manifest/chart comparison skips Helm expressions and the per-release coordinates', () => {
  // version, both urls, serving.selfHosting.chart.version and valuesDoc differ
  // between the two copies in the fixture above and must not be reported.
  const result = fixtureRepo().run()
  assert.equal(only(result, CHECKS.MANIFEST_CHART_PARITY).length, 0)
  const chartSpec = parseYAML(chartCatalogEntryYAML(CHART)).find((document) => document.kind === 'CatalogEntry').spec
  assert.equal(chartSpec.serving.selfHosting.releaseName, 'fixture')
  assert.equal(chartSpec.serving.ui.children[0].builtinRoute, 'widgets')
  assert.equal(chartSpec.export.resources[0].actions[0].limits.timeoutSeconds, 180)
  assert.equal(chartSpec.export.resources[0].actions[0].version, 'v1')
})

test('a spec field that drifts from the chart is reported', () => {
  const result = fixtureRepo({
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': CHART.replace('healthPath: "/readyz"', 'healthPath: "/healthz"'),
  }).run()
  const parity = only(result, CHECKS.MANIFEST_CHART_PARITY)
  assert.equal(parity.length, 1)
  assert.match(parity[0].message, /spec\.serving\.backend\.healthPath/)
  assert.match(parity[0].message, /\/readyz/)
})

test('a field present in only one copy is reported', () => {
  const result = fixtureRepo({
    'providers/fixture/manifest.yaml': MANIFEST.replace('  category: "Demo"\n', '  category: "Demo"\n  hub:\n    access:\n      - capability: memberships.read\n'),
  }).run()
  const parity = only(result, CHECKS.MANIFEST_CHART_PARITY)
  assert.equal(parity.length, 1)
  assert.match(parity[0].message, /spec\.hub: only manifest\.yaml sets it/)
})

test('a missing chart template is a parity violation, not a crash', () => {
  const result = fixtureRepo({ 'providers/fixture/deploy/chart/templates/catalogentry.yaml': null }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.MANIFEST_CHART_PARITY && /no chart CatalogEntry template/.test(item.message)))
})

// ---------------------------------------------------------------------------
// claim-selector
// ---------------------------------------------------------------------------

test('a core-group secrets requirement with no selector is reported', () => {
  // The whole point of X-4: an unscoped `secrets` claim is per RESOURCE, so it
  // reaches every Secret in every workspace that enables the provider. It
  // looks identical to a scoped one in review, which is why it is checked.
  const result = fixtureRepo({
    'providers/fixture/manifest.yaml': MANIFEST.replace(SELECTOR, ''),
    'providers/fixture/deploy/chart/templates/catalogentry.yaml': CHART.replace(SELECTOR, ''),
    [EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
  }).run()
  const scoped = only(result, CHECKS.CLAIM_SELECTOR)
  assert.equal(scoped.length, 1)
  assert.match(scoped[0].message, /core resource secrets with no selector\.matchLabels/)
  assert.match(scoped[0].message, /railgrid\.ai\/owner/)
  // Manifest and export agree that it is unscoped, so parity stays quiet: the
  // two checks answer different questions.
  assert.equal(only(result, CHECKS.CLAIMS_PARITY).length, 0)
})

test('a non-core requirement needs no selector', () => {
  // clusterroles.rbac.authorization.k8s.io is required unscoped in the fixture
  // and must not be reported: the rule is about core-group credential
  // material, not about every claim.
  assert.equal(only(fixtureRepo().run(), CHECKS.CLAIM_SELECTOR).length, 0)
})

// ---------------------------------------------------------------------------
// claims-parity: spec.requires against the generated APIExport
// ---------------------------------------------------------------------------

test('a selector on only one of the manifest and the export is a parity violation', () => {
  const result = fixtureRepo({
    [EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(UNSCOPED_FROM, UNSCOPED_TO),
  }).run()
  const parity = only(result, CHECKS.CLAIMS_PARITY)
  assert.equal(parity.length, 2)
  assert.ok(parity.some((item) => /scoped railgrid\.ai\/owner=fixture but the generated APIExport does not claim it/.test(item.message)))
  assert.ok(parity.some((item) => /claims secrets .* scoped \* but manifest\.yaml spec\.requires does not declare it/.test(item.message)))
})

test('a selector whose value drifts between the manifest and the export is reported', () => {
  const drifted = GENERATED_EXPORT.replace('railgrid.ai/owner: fixture', 'railgrid.ai/owner: somebody-else')
  const result = fixtureRepo({ [EXPORT_PATH]: drifted, [CHART_EXPORT_PATH]: drifted }).run()
  assert.equal(only(result, CHECKS.CLAIMS_PARITY).length, 2)
})

test('a requirement the generated APIExport does not claim is reported', () => {
  const dropped = GENERATED_EXPORT.replace('  - group: rbac.authorization.k8s.io\n    resource: clusterroles\n    verbs:\n    - get\n    - create\n', '')
  const result = fixtureRepo({ [EXPORT_PATH]: dropped, [CHART_EXPORT_PATH]: dropped }).run()
  const claims = only(result, CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /manifest\.yaml requires clusterroles\.rbac\.authorization\.k8s\.io \[create get\] scoped \* but the generated APIExport does not claim it/)
})

test('a claim only the generated APIExport carries is reported', () => {
  const extra = withClaims('  - resource: configmaps\n    verbs:\n    - get\n')
  const result = fixtureRepo({ [EXPORT_PATH]: extra, [CHART_EXPORT_PATH]: extra }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY
    && /the generated APIExport claims configmaps \[get\] scoped \* but manifest\.yaml spec\.requires does not declare it/.test(item.message)))
})

test('differing verbs on the same resource are two reports, not a silent pass', () => {
  const narrowed = GENERATED_EXPORT.replace('    - get\n    - list\n    - watch\n', '    - get\n    - list\n')
  const result = fixtureRepo({ [EXPORT_PATH]: narrowed, [CHART_EXPORT_PATH]: narrowed }).run()
  const claims = only(result, CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 2)
  assert.ok(claims.some((item) => /manifest\.yaml requires secrets \[get list watch\]/.test(item.message)))
  assert.ok(claims.some((item) => /the generated APIExport claims secrets \[get list\]/.test(item.message)))
})

test('a verb coordinate carries no verbs in the manifest and every verb in the claim', () => {
  // The one asymmetry left in the comparison: the verb IS the capability, so
  // the requirement lists none and the generated claim spells "*".
  assert.deepEqual(pairedRepo().run().violations, [])

  const narrowed = PARTNER_CLAIMS.replace("    - '*'\n", '    - create\n')
  const result = pairedRepo({}, { claims: narrowed }).run()
  const claims = only(result, CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 2)
  assert.ok(claims.some((item) => /requires instances\/exec\.partner\.railgrid\.ai \[\*\]/.test(item.message)))
  assert.ok(claims.some((item) => /claims instances\/exec\.partner\.railgrid\.ai \[create\]/.test(item.message)))
})

// The export's name is the other half of the contract: the CatalogEntry points
// tenants at spec.export.name, and that is the object init must create.
test('an APIExport named after something other than spec.export.name is reported', () => {
  const renamed = GENERATED_EXPORT.replace('  name: fixture.providers.railgrid.ai\n', '  name: fixture.railgrid.ai\n')
  const result = fixtureRepo({ [EXPORT_PATH]: renamed, [CHART_EXPORT_PATH]: renamed }).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY && /generated APIExport is named "fixture\.railgrid\.ai"/.test(item.message)))
})

test('a manifest with no spec.export.name is reported, not a crash', () => {
  const stripped = (text) => text.replace(/  export:\n    name: "[^"]*"\n/, '  export:\n')
  const result = mutatedRepo(stripped).run()
  assert.ok(result.violations.some((item) => item.check === CHECKS.CLAIMS_PARITY && /declares no spec\.export\.name/.test(item.message)))
})

test('a missing generated APIExport is reported, not a crash', () => {
  const result = fixtureRepo({ [EXPORT_PATH]: null, [CHART_EXPORT_PATH]: null }).run()
  const claims = only(result, CHECKS.CLAIMS_PARITY)
  assert.equal(claims.length, 1)
  assert.match(claims[0].message, /no generated APIExport at .*apiexport-fixture\.providers\.railgrid\.ai\.yaml; run make codegen-fixture-provider/)
  // export-copy stays quiet: there is nothing to copy yet.
  assert.equal(only(result, CHECKS.EXPORT_COPY).length, 0)
  // ... and so does export-group: there is no served-group list to check against.
  assert.equal(only(result, CHECKS.EXPORT_GROUP).length, 0)
})

// ---------------------------------------------------------------------------
// subresource-access-claim
// ---------------------------------------------------------------------------

test('an export that publishes a coordinate must require subjectaccessreviews', () => {
  // Dropped from the requirement list AND from the generated claims, so this is
  // the only thing reported: the two checks answer different questions.
  const stripped = (text) => text.replace(ACCESS_REQUIREMENT, '')
  const withoutClaim = GENERATED_EXPORT.replace(ACCESS_CLAIM, '')
  const reported = only(mutatedRepo(stripped, {
    [EXPORT_PATH]: withoutClaim,
    [CHART_EXPORT_PATH]: withoutClaim,
  }).run(), CHECKS.SUBRESOURCE_ACCESS_CLAIM)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /"widgets\/greet"/)
  assert.match(reported[0].message, /authorization\.k8s\.io subjectaccessreviews/)
  assert.match(reported[0].message, /spec\.requires/)
  assert.equal(reported[0].path, 'providers/fixture/manifest.yaml')
})

test('an export that publishes no coordinate is not asked for the claim', () => {
  const noActions = (text) => text.replace(/        actions:\n(?:          .*\n|            .*\n)*/, '')
  const result = mutatedRepo(noActions, {
    [EXPORT_PATH]: GENERATED_EXPORT.replace(ACCESS_CLAIM, ''),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(ACCESS_CLAIM, ''),
  }).run()
  // The requirement is still declared and still claimed, which is harmless.
  const stripped = (text) => noActions(text).replace(ACCESS_REQUIREMENT, '')
  const bare = mutatedRepo(stripped, {
    [EXPORT_PATH]: GENERATED_EXPORT.replace(ACCESS_CLAIM, ''),
    [CHART_EXPORT_PATH]: GENERATED_EXPORT.replace(ACCESS_CLAIM, ''),
  }).run()
  assert.equal(only(result, CHECKS.SUBRESOURCE_ACCESS_CLAIM).length, 0)
  assert.equal(only(bare, CHECKS.SUBRESOURCE_ACCESS_CLAIM).length, 0)
})

// ---------------------------------------------------------------------------
// export-group
// ---------------------------------------------------------------------------

test('an export resource apiVersion whose group the export does not serve is reported', () => {
  const wrong = (text) => text.replace('        apiVersion: fixture.railgrid.ai/v1alpha1\n', '        apiVersion: widgets.railgrid.ai/v1alpha1\n')
  const reported = only(mutatedRepo(wrong).run(), CHECKS.EXPORT_GROUP)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /spec\.export\.resources\[0\] \(widgets\)/)
  assert.match(reported[0].message, /serves no widgets\.railgrid\.ai; it serves fixture\.railgrid\.ai/)
  assert.equal(reported[0].path, 'providers/fixture/manifest.yaml')
})

test('an export resource apiVersion that is not group/version is reported', () => {
  const bare = (text) => text.replace('        apiVersion: fixture.railgrid.ai/v1alpha1\n', '        apiVersion: v1alpha1\n')
  const reported = only(mutatedRepo(bare).run(), CHECKS.EXPORT_GROUP)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /is not "group\/version"/)
})

test('an export that serves no group yet is not held to its apiVersions', () => {
  // `resources: []` is what a provider minting its schemas at runtime ships;
  // there is no served-group list, and inventing one would report every entry.
  const runtimeMinted = GENERATED_EXPORT.replace(/  resources:\n(?:  [-\s].*\n)*/, '  resources: []\n')
  assert.equal(only(fixtureRepo({ [EXPORT_PATH]: runtimeMinted, [CHART_EXPORT_PATH]: runtimeMinted }).run(), CHECKS.EXPORT_GROUP).length, 0)
})

// ---------------------------------------------------------------------------
// requires-group / requires-verb
// ---------------------------------------------------------------------------

test('a requirement naming a provider and a group that provider serves is accepted', () => {
  const result = pairedRepo().run()
  assert.deepEqual(result.violations, [])
  assert.deepEqual(result.providers, ['fixture', 'partner'])
})

test('a requirement that names the APIExport instead of the group it serves is reported', () => {
  const requirement = PARTNER_REQUIREMENT.replace('group: partner.railgrid.ai', 'group: partner.providers.railgrid.ai')
  const claims = PARTNER_CLAIMS.replaceAll('group: partner.railgrid.ai', 'group: partner.providers.railgrid.ai')
  const reported = only(pairedRepo({}, { requirement, claims }).run(), CHECKS.REQUIRES_GROUP)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /asks provider partner for group partner\.providers\.railgrid\.ai, which partner does not serve; it serves partner\.railgrid\.ai/)
  assert.equal(reported[0].path, 'providers/fixture/manifest.yaml')
})

test('a requirement naming a provider that is not in the tree is reported', () => {
  const requirement = PARTNER_REQUIREMENT.replace('provider: partner', 'provider: stranger')
  const reported = only(pairedRepo({}, { requirement }).run(), CHECKS.REQUIRES_GROUP)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /names provider "stranger", which is no provider in this tree/)
})

test('a requirement on a provider with no group is reported', () => {
  const requirement = PARTNER_REQUIREMENT.replace('      group: partner.railgrid.ai\n', '')
  const claims = PARTNER_CLAIMS.replaceAll('    group: partner.railgrid.ai\n', '')
  const reported = only(pairedRepo({}, { requirement, claims }).run(), CHECKS.REQUIRES_GROUP)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /names provider partner but no group/)
})

test('a required coordinate its owner does not declare is reported', () => {
  // The check the old shape could not make: the claim lived in one manifest and
  // the declaration in another, so nothing held them together.
  const requirement = PARTNER_REQUIREMENT.replace('name: instances/exec', 'name: instances/reboot')
  const claims = PARTNER_CLAIMS.replace('resource: instances/exec', 'resource: instances/reboot')
  const reported = only(pairedRepo({}, { requirement, claims }).run(), CHECKS.REQUIRES_VERB)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /claims the coordinate instances\/reboot in partner\.railgrid\.ai, but partner declares no such verb or action on it/)
  assert.match(reported[0].message, /instances\/exec, instances\/snapshot/)
})

test("a required coordinate naming its owner's ACTION is accepted", () => {
  // An action is a coordinate like any other, and its version is not part of
  // it: `snapshot` v1 publishes `instances/snapshot`.
  const requirement = PARTNER_REQUIREMENT.replace('name: instances/exec', 'name: instances/snapshot')
  const claims = PARTNER_CLAIMS.replace('resource: instances/exec', 'resource: instances/snapshot')
  assert.deepEqual(pairedRepo({}, { requirement, claims }).run().violations, [])
})

test('a required coordinate whose owner declares nothing at all is still reported', () => {
  const bare = PARTNER_MANIFEST.replace(/        verbs:\n(?:          .*\n)*        actions:\n(?:          .*\n|            .*\n)*/, '')
  const reported = only(pairedRepo(partnerFiles(bare)).run(), CHECKS.REQUIRES_VERB)
  assert.equal(reported.length, 1)
  assert.match(reported[0].message, /spec\.export publishes none/)
})

test('a platform builtin requirement names no provider and is cross-checked against nothing', () => {
  // authorization.k8s.io and the core group are served by no provider; the
  // fixture requires both and neither is reported.
  const result = fixtureRepo().run()
  assert.equal(only(result, CHECKS.REQUIRES_GROUP).length, 0)
  assert.equal(only(result, CHECKS.REQUIRES_VERB).length, 0)
})

// ---------------------------------------------------------------------------
// the checks that do not read the manifest
// ---------------------------------------------------------------------------

test('the chart copy of the APIExport must be byte-identical', () => {
  const missing = fixtureRepo({ [CHART_EXPORT_PATH]: null }).run()
  assert.ok(missing.violations.some((item) => item.check === CHECKS.EXPORT_COPY && /the chart ships no apiexport\.yaml/.test(item.message)))

  const drifted = fixtureRepo({ [CHART_EXPORT_PATH]: `${GENERATED_EXPORT}# hand-edited\n` }).run()
  const copy = only(drifted, CHECKS.EXPORT_COPY)
  assert.equal(copy.length, 1)
  assert.match(copy[0].message, /the chart copy is an output, run make codegen-fixture-provider/)
  assert.equal(copy[0].path, CHART_EXPORT_PATH)
  // The content is still equivalent, so claims parity has nothing to say.
  assert.equal(only(drifted, CHECKS.CLAIMS_PARITY).length, 0)
})

test('both READMEs are required', () => {
  const result = fixtureRepo({ 'providers/fixture/README.md': null, 'providers/fixture/deploy/chart/README.md': null }).run()
  assert.deepEqual(only(result, CHECKS.README_MISSING).map((item) => item.message), ['deploy/chart/README.md is missing', 'README.md is missing'])
})

test('ad-hoc /api/ routes are reported with file:line, in every route dialect', () => {
  const result = fixtureRepo({
    'providers/fixture/main.go': `${MAIN_GO}\nfunc more() {\n\tmux.HandleFunc("/api/hello", hello)\n}\n`,
    'providers/fixture/server/routes.go': 'package server\n\nfunc routes() {\n\tmux.HandleFunc("GET /api/widgets", list)\n}\n',
    'providers/fixture/api/legacy.go': 'package api\n\nfunc legacy() {\n\trouter.PathPrefix("/api").Handler(h)\n}\n',
    'providers/fixture/api/legacy_test.go': 'package api\n\nfunc TestX() { call("/api/not-a-route") }\n',
    'providers/fixture/server/doc.go': 'package server\n\n// Historic: "/api/gone" was removed.\n/* and "/api/also-gone" */\n',
  }).run()
  const routes = only(result, CHECKS.ADHOC_REST)
  assert.deepEqual(
    routes.map((item) => `${item.path}:${item.line}`),
    ['providers/fixture/api/legacy.go:4', 'providers/fixture/main.go:10', 'providers/fixture/server/routes.go:4'],
  )
  assert.ok(routes.some((item) => item.message.includes('"/api/widgets"')))
  assert.ok(routes.some((item) => item.message.includes('"/api"')))
})

// ---------------------------------------------------------------------------
// the excuse registry
// ---------------------------------------------------------------------------

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

test('a cross-reference check can be excused, for a provider outside this tree', () => {
  const requirement = PARTNER_REQUIREMENT.replace('provider: partner', 'provider: stranger')
  const result = pairedRepo({}, { requirement }).run({
    version: 1,
    exceptions: [{ provider: 'fixture', check: CHECKS.REQUIRES_GROUP, reason: 'stranger ships from the external provider repo' }],
  })
  assert.deepEqual(result.violations, [])
  assert.equal(result.excused.length, 1)
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
  assert.throws(() => fixture.run({ version: 1, exceptions: [{ provider: 'fixture', check: CHECKS.SUBRESOURCE_ACCESS_CLAIM, reason: 'r' }] }), /is not an exceptable check/)
})

// ---------------------------------------------------------------------------
// the YAML subset, and the real tree
// ---------------------------------------------------------------------------

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

/**
 * Whether the in-tree manifests have been rewritten into the new shape.
 *
 * The seven of them are migrated separately from this scanner, and until they
 * land every one of them reports "declares no spec.export.name". The tree test
 * below is skipped while that is true rather than deleted: it is the only thing
 * that holds the real providers to the contract, and it must come back the
 * moment they land.
 */
const IN_TREE_SHAPE = (() => {
  const root = path.join(REPO_ROOT, 'providers')
  if (!fs.existsSync(root)) return { migrated: false, stale: ['no providers/ directory'] }
  const stale = []
  for (const name of fs.readdirSync(root).sort()) {
    const manifest = path.join(root, name, 'manifest.yaml')
    if (!fs.existsSync(manifest)) continue
    const entry = parseYAML(fs.readFileSync(manifest, 'utf8')).find((document) => document && document.kind === 'CatalogEntry')
    if (!entry?.spec?.export?.name) stale.push(name)
  }
  return { migrated: stale.length === 0, stale }
})()

const TREE_SKIP = IN_TREE_SHAPE.migrated
  ? false
  : `the in-tree manifests are still in the old CatalogEntry shape (${IN_TREE_SHAPE.stale.join(', ')}); re-enable once they land`

test('the real tree passes with the checked-in exception registry', { skip: TREE_SKIP }, () => {
  const result = verify()
  assert.deepEqual(result.violations.map((item) => `${item.provider} ${item.check}: ${item.message}`), [])
  assert.ok(result.providers.length >= 7, `expected the in-tree providers, got ${result.providers.join(', ')}`)
})
