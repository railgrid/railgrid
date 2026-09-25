import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  CHECKS,
  POLICY_PATH,
  PROVIDER_SUBJECTS,
  check,
  generate,
  write,
} from './generate-permission-claim-policy.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')

// A minimal manifest: only what the generator reads (spec.apiExport.name and
// spec.dependencies[].composes[].group) plus enough of a CatalogEntry to be one.
function manifest({ exportName = 'fixture.providers.railgrid.ai', dependencies = '' } = {}) {
  return `apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  displayName: "Fixture"
${dependencies}  apiExport:
    name: "${exportName}"
`
}

const COMPOSES_CODE = `  dependencies:
    - name: code
      composes:
        - group: code.railgrid.ai
          resource: repositories
          verbs: ["get", "list"]
`

const COMPOSES_CODE_AND_EDGES = `  dependencies:
    - name: code
      composes:
        - group: code.railgrid.ai
          resource: repositories
          verbs: ["get", "list"]
    - name: edges
      composes:
        - group: edges.railgrid.ai
          resource: kubernetesclusters
          verbs: ["get"]
`

function apiExport({ name = 'fixture.providers.railgrid.ai', groups = ['fixture.railgrid.ai'] } = {}) {
  const resources = groups.length
    ? groups.map((group) => `  - group: ${group}\n    name: widgets\n    schema: v1.widgets.${group}\n    storage:\n      crd: {}`).join('\n')
    : '  resources: []'
  return `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: ${name}
spec:
  permissionClaims: []
${groups.length ? `  resources:\n${resources}` : resources}
`
}

/**
 * A throwaway checkout with one in-tree provider, and optionally a second tree
 * standing in for the external provider repo (same providers/<name>/ layout).
 */
function fixtureRepo(overrides = {}, { external = null } = {}) {
  const repoRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'railgrid-claim-policy-'))
  const files = {
    'providers/fixture/manifest.yaml': manifest({ dependencies: COMPOSES_CODE }),
    'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': apiExport(),
    ...overrides,
  }
  for (const [relative, content] of Object.entries(files)) {
    if (content === null) continue
    const absolute = path.join(repoRoot, relative)
    fs.mkdirSync(path.dirname(absolute), { recursive: true })
    fs.writeFileSync(absolute, content)
  }
  let externalDir = null
  if (external) {
    externalDir = fs.mkdtempSync(path.join(os.tmpdir(), 'railgrid-claim-policy-ext-'))
    for (const [relative, content] of Object.entries(external)) {
      const absolute = path.join(externalDir, relative)
      fs.mkdirSync(path.dirname(absolute), { recursive: true })
      fs.writeFileSync(absolute, content)
    }
  }
  return {
    repoRoot,
    externalDir,
    generate: () => generate({ repoRoot, externalDir }),
    check: () => check({ repoRoot, externalDir }),
    write: (options = {}) => write({ repoRoot, externalDir, ...options }),
    policy: () => fs.readFileSync(path.join(repoRoot, POLICY_PATH), 'utf8'),
    put(relative, content) {
      const absolute = path.join(repoRoot, relative)
      fs.mkdirSync(path.dirname(absolute), { recursive: true })
      fs.writeFileSync(absolute, content)
    },
  }
}

function claims(result) {
  return result.policy.spec.claims
}

test('the claimed groups come from composes and the claimer from the generated APIExport', () => {
  const result = fixtureRepo().generate()
  assert.deepEqual(claims(result), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  // The manifest names the export `fixture.providers.railgrid.ai`; the claimer
  // is the group it SERVES. Nothing may infer one from the other.
  assert.notEqual(claims(result)[0].claimer, 'fixture.providers.railgrid.ai')
  assert.equal(result.relationships, 1)
})

test('a new composes entry changes the output', () => {
  const before = fixtureRepo().generate()
  const after = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ dependencies: COMPOSES_CODE_AND_EDGES }) }).generate()
  assert.deepEqual(claims(before), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  assert.deepEqual(claims(after), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai', 'edges.railgrid.ai'] }])
  assert.notEqual(before.yaml, after.yaml)
  assert.match(after.yaml, /^    - edges\.railgrid\.ai$/m)
  assert.equal(after.relationships, 2)
})

test('composed groups are deduplicated and sorted, and a self-claim is dropped', () => {
  const dependencies = `  dependencies:
    - name: code
      composes:
        - group: code.railgrid.ai
          resource: repositories
          verbs: ["get"]
        - group: code.railgrid.ai
          resource: repositorycommits
          verbs: ["get"]
    - name: self
      composes:
        - group: fixture.railgrid.ai
          resource: widgets
          verbs: ["get"]
`
  const result = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ dependencies }) }).generate()
  assert.deepEqual(claims(result), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  // Every composes entry still counts as a relationship, deduplication or not.
  assert.equal(result.relationships, 3)
})

test('a provider that composes nothing produces no rule but is still scanned', () => {
  const fixture = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest() })
  const result = fixture.generate()
  assert.deepEqual(claims(result), [])
  // Its exported group is known all the same: that is what lets --check tell a
  // stale rule from one belonging to a provider outside this checkout.
  assert.ok(result.claimerGroups.has('fixture.railgrid.ai'))
})

test('a provider whose exported group cannot be derived fails, it is never guessed', () => {
  // resources: [] is what a provider that mints its schemas at runtime ships
  // (the infrastructure provider). Harmless until it composes something.
  const empty = { 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': apiExport({ groups: [] }) }
  assert.throws(() => fixtureRepo(empty).generate(), /composes 1 api group\(s\) but the api group it exports is unknown.*serves no spec\.resources\[\]\.group/s)
  // ... and harmless when the provider composes nothing.
  assert.deepEqual(claims(fixtureRepo({ ...empty, 'providers/fixture/manifest.yaml': manifest() }).generate()), [])

  assert.throws(
    () => fixtureRepo({ 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': null }).generate(),
    /no generated APIExport at providers\/fixture\/config\/kcp\/apiexport-fixture\.providers\.railgrid\.ai\.yaml/,
  )
  assert.throws(
    () => fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ dependencies: COMPOSES_CODE }).replace(/  apiExport:\n    name: "[^"]*"\n/, '') }).generate(),
    /declares no spec\.apiExport\.name/,
  )
  assert.throws(
    () => fixtureRepo({ 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': 'apiVersion: v1\nkind: ConfigMap\n' }).generate(),
    /has no kind: APIExport document/,
  )
})

test('an external providers directory is scanned the same way, and its name is not in the file', () => {
  const external = {
    'providers/outsider/manifest.yaml': manifest({ exportName: 'outsider.providers.railgrid.ai', dependencies: COMPOSES_CODE }),
    'providers/outsider/config/kcp/apiexport-outsider.providers.railgrid.ai.yaml': apiExport({ name: 'outsider.providers.railgrid.ai', groups: ['outsider.providers.railgrid.ai'] }),
  }
  const fixture = fixtureRepo({}, { external })
  const result = fixture.generate()
  assert.deepEqual(claims(result).map((rule) => rule.claimer), ['fixture.railgrid.ai', 'outsider.providers.railgrid.ai'])
  assert.match(result.yaml, /^#   external:providers\/outsider$/m)
  // The checkout lives in a temp dir; its path must never reach the file.
  assert.ok(!result.yaml.includes(fixture.externalDir))
})

test('a provider defined in both trees is an error, not a last-one-wins merge', () => {
  const external = {
    'providers/fixture/manifest.yaml': manifest({ dependencies: COMPOSES_CODE_AND_EDGES }),
    'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': apiExport(),
  }
  assert.throws(() => fixtureRepo({}, { external }).generate(), /provider fixture is defined twice/)
})

test('--check detects a composes entry nobody whitelisted', () => {
  const fixture = fixtureRepo()
  fixture.write()
  assert.deepEqual(fixture.check().violations, [])

  // The edge is declared; the committed file has not caught up.
  fixture.put('providers/fixture/manifest.yaml', manifest({ dependencies: COMPOSES_CODE_AND_EDGES }))
  const stale = fixture.check()
  assert.equal(stale.violations.length, 1)
  assert.equal(stale.violations[0].check, CHECKS.DRIFTED_RULE)
  assert.match(stale.violations[0].message, /claimer fixture\.railgrid\.ai whitelists \[code\.railgrid\.ai\] but the manifests compose \[code\.railgrid\.ai edges\.railgrid\.ai\]/)
  assert.equal(stale.violations[0].path, POLICY_PATH)

  fixture.write()
  assert.deepEqual(fixture.check().violations, [])
})

test('--check reports a claimer with no rule at all, and a rule no manifest backs', () => {
  const fixture = fixtureRepo()
  fixture.write()
  const committed = fixture.policy()

  fixture.put(POLICY_PATH, committed.replace(/  claims:\n(?:.*\n)*?  providers:/, '  claims:\n  - claimer: other.railgrid.ai\n    groups:\n    - code.railgrid.ai\n  providers:'))
  const result = fixture.check()
  assert.deepEqual(result.violations.map((item) => item.check).sort(), [CHECKS.MISSING_RULE])
  assert.match(result.violations[0].message, /has no rule for claimer fixture\.railgrid\.ai/)
  // other.railgrid.ai is exported by no scanned provider, so it is left alone.
  assert.deepEqual(result.unscanned, ['other.railgrid.ai'])
})

test('--check calls out a rule for a scanned provider that no longer composes', () => {
  const fixture = fixtureRepo()
  fixture.write()
  fixture.put('providers/fixture/manifest.yaml', manifest())
  const result = fixture.check()
  assert.equal(result.violations.length, 1)
  assert.equal(result.violations[0].check, CHECKS.STALE_RULE)
  assert.match(result.violations[0].message, /claimer fixture\.railgrid\.ai .* the rule is stale/)
})

test('--check reports a missing file and drifted spec.providers', () => {
  const missing = fixtureRepo().check()
  assert.deepEqual(missing.violations.map((item) => item.check), [CHECKS.MISSING_FILE])

  const fixture = fixtureRepo()
  fixture.write()
  fixture.put(POLICY_PATH, fixture.policy().replace('system:serviceaccount:default:provider', 'system:serviceaccount:default:someone-else'))
  assert.deepEqual(fixture.check().violations.map((item) => item.check), [CHECKS.DRIFTED_SUBJECTS])

  fixture.write()
  fixture.put(POLICY_PATH, fixture.policy().replace('  name: railgrid', '  name: something-else'))
  assert.deepEqual(fixture.check().violations.map((item) => item.check), [CHECKS.DRIFTED_HEADER])
})

test('writing refuses to drop a rule this scan could not see', () => {
  const external = {
    'providers/outsider/manifest.yaml': manifest({ exportName: 'outsider.providers.railgrid.ai', dependencies: COMPOSES_CODE }),
    'providers/outsider/config/kcp/apiexport-outsider.providers.railgrid.ai.yaml': apiExport({ name: 'outsider.providers.railgrid.ai', groups: ['outsider.providers.railgrid.ai'] }),
  }
  const full = fixtureRepo({}, { external })
  full.write()

  // The same checkout, rescanned without the external tree: the outsider's rule
  // is still there and must survive.
  const partial = { repoRoot: full.repoRoot }
  assert.deepEqual(check(partial).violations, [])
  assert.deepEqual(check(partial).unscanned, ['outsider.providers.railgrid.ai'])
  assert.throws(() => write(partial), /writing would drop 1 rule\(s\).*outsider\.providers\.railgrid\.ai/s)
  write({ ...partial, dropUnscanned: true })
  assert.deepEqual(claims(check(partial)).map((rule) => rule.claimer), ['fixture.railgrid.ai'])
})

test('writing is idempotent', () => {
  const fixture = fixtureRepo()
  assert.equal(fixture.write().changed, true)
  assert.equal(fixture.write().changed, false)
})

test('spec.providers stays in lockstep with the ServiceAccount the hub provisions', () => {
  // The subject is not a choice this generator makes; it is the identity
  // pkg/hub/providers mints for every provider. If those constants move, this
  // file has to move with them.
  const provision = fs.readFileSync(path.join(REPO_ROOT, 'pkg/hub/providers/provision.go'), 'utf8')
  const name = provision.match(/const ProviderSAName = "([^"]+)"/)
  const namespace = provision.match(/const ProviderSANamespace = "([^"]+)"/)
  assert.ok(name && namespace, 'ProviderSAName/ProviderSANamespace not found in pkg/hub/providers/provision.go')
  assert.deepEqual(PROVIDER_SUBJECTS.map((subject) => ({ ...subject })), [
    { kind: 'User', name: `system:serviceaccount:${namespace[1]}:${name[1]}` },
  ])
})

test('the committed policy matches this tree', () => {
  const result = check({ repoRoot: REPO_ROOT })
  assert.deepEqual(result.violations, [], result.violations.map((item) => item.message).join('\n'))
})
