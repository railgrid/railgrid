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
import { parseYAML } from './verify-provider-contract.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')

// A minimal manifest: only what the generator reads (spec.export.name and
// spec.requires[]) plus enough of a CatalogEntry to be one.
function manifest({ exportName = 'fixture.providers.railgrid.ai', requires = '' } = {}) {
  return `apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  displayName: "Fixture"
${requires}  export:
    name: "${exportName}"
`
}

const REQUIRES_CODE = `  requires:
    - provider: code
      group: code.railgrid.ai
      resources:
        - name: repositories
          verbs: ["get", "list"]
`

const REQUIRES_CODE_AND_EDGES = `  requires:
    - provider: code
      group: code.railgrid.ai
      resources:
        - name: repositories
          verbs: ["get", "list"]
    - provider: edges
      group: edges.railgrid.ai
      resources:
        - name: kubernetesclusters
          verbs: ["get"]
        - name: kubernetesclusters/k8s
`

// The requirements that name NO provider: the platform builtins and the core
// group. Nobody exports them, so no rule here admits a claim on them.
const REQUIRES_BUILTINS = `  requires:
    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
    - resources:
        - name: secrets
          verbs: [get, list]
          selector:
            matchLabels:
              railgrid.ai/owner: fixture
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
    'providers/fixture/manifest.yaml': manifest({ requires: REQUIRES_CODE }),
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

test('the claimed groups come from spec.requires and the claimer from the generated APIExport', () => {
  const result = fixtureRepo().generate()
  assert.deepEqual(claims(result), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  // The manifest names the export `fixture.providers.railgrid.ai`; the claimer
  // is the group it SERVES. Nothing may infer one from the other.
  assert.notEqual(claims(result)[0].claimer, 'fixture.providers.railgrid.ai')
  assert.equal(result.relationships, 1)
})

test('a new requires entry changes the output', () => {
  const before = fixtureRepo().generate()
  const after = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires: REQUIRES_CODE_AND_EDGES }) }).generate()
  assert.deepEqual(claims(before), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  assert.deepEqual(claims(after), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai', 'edges.railgrid.ai'] }])
  assert.notEqual(before.yaml, after.yaml)
  assert.match(after.yaml, /^    - edges\.railgrid\.ai$/m)
  assert.equal(after.relationships, 2)
})

test('a requirement on a platform builtin produces no rule at all', () => {
  // authorization.k8s.io and the core group are served by no provider, so a
  // rule admitting a claim on them would reserve a Kubernetes builtin group for
  // this platform's providers. Only a cross-PROVIDER claim is policy.
  const result = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires: REQUIRES_BUILTINS }) }).generate()
  assert.deepEqual(claims(result), [])
  assert.equal(result.relationships, 0)
  assert.ok(result.claimerGroups.has('fixture.railgrid.ai'))
})

test('a builtin requirement next to a cross-provider one contributes nothing', () => {
  const mixed = `${REQUIRES_CODE}    - group: authorization.k8s.io
      resources:
        - name: subjectaccessreviews
          verbs: [create]
`
  const result = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires: mixed }) }).generate()
  assert.deepEqual(claims(result), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  assert.equal(result.relationships, 1)
})

test('a requirement on the provider itself is dropped, and the groups are sorted', () => {
  const requires = `  requires:
    - provider: code
      group: code.railgrid.ai
      resources:
        - name: repositories
          verbs: ["get"]
        - name: repositories/commit
    - provider: fixture
      group: fixture.railgrid.ai
      resources:
        - name: widgets
          verbs: ["get"]
`
  const result = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires }) }).generate()
  // An APIExport always has its own identity, so claiming one's own group needs
  // no whitelisting.
  assert.deepEqual(claims(result), [{ claimer: 'fixture.railgrid.ai', groups: ['code.railgrid.ai'] }])
  // Every cross-provider requires entry still counts as a relationship.
  assert.equal(result.relationships, 2)
})

test('a provider that requires nothing from another provider produces no rule but is still scanned', () => {
  const fixture = fixtureRepo({ 'providers/fixture/manifest.yaml': manifest() })
  const result = fixture.generate()
  assert.deepEqual(claims(result), [])
  // Its exported group is known all the same: that is what lets --check tell a
  // stale rule from one belonging to a provider outside this checkout.
  assert.ok(result.claimerGroups.has('fixture.railgrid.ai'))
})

test('a requirement naming a provider with no group is an error', () => {
  const requires = REQUIRES_CODE.replace('      group: code.railgrid.ai\n', '')
  assert.throws(
    () => fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires }) }).generate(),
    /names provider code with no group/,
  )
})

test('a provider whose exported group cannot be derived fails, it is never guessed', () => {
  // resources: [] is what a provider that mints its schemas at runtime ships
  // (the infrastructure provider). Harmless until it requires something.
  const empty = { 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': apiExport({ groups: [] }) }
  assert.throws(() => fixtureRepo(empty).generate(), /requires 1 api group\(s\) from other providers but the api group it exports is unknown.*serves no spec\.resources\[\]\.group/s)
  // ... and harmless when the provider requires nothing from another provider.
  assert.deepEqual(claims(fixtureRepo({ ...empty, 'providers/fixture/manifest.yaml': manifest() }).generate()), [])

  assert.throws(
    () => fixtureRepo({ 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': null }).generate(),
    /no generated APIExport at providers\/fixture\/config\/kcp\/apiexport-fixture\.providers\.railgrid\.ai\.yaml/,
  )
  assert.throws(
    () => fixtureRepo({ 'providers/fixture/manifest.yaml': manifest({ requires: REQUIRES_CODE }).replace(/  export:\n    name: "[^"]*"\n/, '') }).generate(),
    /declares no spec\.export\.name/,
  )
  assert.throws(
    () => fixtureRepo({ 'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': 'apiVersion: v1\nkind: ConfigMap\n' }).generate(),
    /has no kind: APIExport document/,
  )
})

test('an external providers directory is scanned the same way, and its name is not in the file', () => {
  const external = {
    'providers/outsider/manifest.yaml': manifest({ exportName: 'outsider.providers.railgrid.ai', requires: REQUIRES_CODE }),
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
    'providers/fixture/manifest.yaml': manifest({ requires: REQUIRES_CODE_AND_EDGES }),
    'providers/fixture/config/kcp/apiexport-fixture.providers.railgrid.ai.yaml': apiExport(),
  }
  assert.throws(() => fixtureRepo({}, { external }).generate(), /provider fixture is defined twice/)
})

test('--check detects a requires entry nobody whitelisted', () => {
  const fixture = fixtureRepo()
  fixture.write()
  assert.deepEqual(fixture.check().violations, [])

  // The edge is declared; the committed file has not caught up.
  fixture.put('providers/fixture/manifest.yaml', manifest({ requires: REQUIRES_CODE_AND_EDGES }))
  const stale = fixture.check()
  assert.equal(stale.violations.length, 1)
  assert.equal(stale.violations[0].check, CHECKS.DRIFTED_RULE)
  assert.match(stale.violations[0].message, /claimer fixture\.railgrid\.ai whitelists \[code\.railgrid\.ai\] but the manifests require \[code\.railgrid\.ai edges\.railgrid\.ai\]/)
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
  assert.match(result.violations[0].message, /a spec\.requires entry declares code\.railgrid\.ai/)
  // other.railgrid.ai is exported by no scanned provider, so it is left alone.
  assert.deepEqual(result.unscanned, ['other.railgrid.ai'])
})

test('--check calls out a rule for a scanned provider that no longer requires anything', () => {
  const fixture = fixtureRepo()
  fixture.write()
  fixture.put('providers/fixture/manifest.yaml', manifest())
  const result = fixture.check()
  assert.equal(result.violations.length, 1)
  assert.equal(result.violations[0].check, CHECKS.STALE_RULE)
  assert.match(result.violations[0].message, /claimer fixture\.railgrid\.ai .* the rule is stale/)
})

test('--check calls out a rule left over from a requirement that became a builtin', () => {
  // Dropping the `provider` off a requirement is not cosmetic: it stops being a
  // cross-provider claim, so its rule has to go with it.
  const fixture = fixtureRepo()
  fixture.write()
  fixture.put('providers/fixture/manifest.yaml', manifest({ requires: REQUIRES_BUILTINS }))
  const result = fixture.check()
  assert.deepEqual(result.violations.map((item) => item.check), [CHECKS.STALE_RULE])
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
    'providers/outsider/manifest.yaml': manifest({ exportName: 'outsider.providers.railgrid.ai', requires: REQUIRES_CODE }),
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

test('the emitted header says where each field comes from', () => {
  const { yaml } = fixtureRepo().generate()
  assert.match(yaml, /spec\.claims\[\]\.groups  comes from each provider's manifest\.yaml/)
  assert.match(yaml, /spec\.requires\[\]\.group, for the entries that name a/)
  assert.match(yaml, /^# Generated from 1 provider manifest\(s\), 1 cross-provider requirement\(s\):$/m)
  assert.match(yaml, /^#   providers\/fixture$/m)
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

/**
 * Whether the in-tree manifests have been rewritten into the new CatalogEntry
 * shape. Until they have, spec.requires is nowhere and a regeneration would
 * empty the committed policy, so the tree test below is skipped rather than
 * deleted: it is the only thing that holds the committed file to the real
 * manifests, and it must come back the moment they land.
 */
const IN_TREE_SHAPE = (() => {
  const root = path.join(REPO_ROOT, 'providers')
  if (!fs.existsSync(root)) return { migrated: false, stale: ['no providers/ directory'] }
  const stale = []
  for (const name of fs.readdirSync(root).sort()) {
    const file = path.join(root, name, 'manifest.yaml')
    if (!fs.existsSync(file)) continue
    const entry = parseYAML(fs.readFileSync(file, 'utf8')).find((document) => document && document.kind === 'CatalogEntry')
    if (!entry?.spec?.export?.name) stale.push(name)
  }
  return { migrated: stale.length === 0, stale }
})()

const TREE_SKIP = IN_TREE_SHAPE.migrated
  ? false
  : `the in-tree manifests are still in the old CatalogEntry shape (${IN_TREE_SHAPE.stale.join(', ')}); re-run make permission-claim-policy and re-enable once they land`

test('the committed policy matches this tree', { skip: TREE_SKIP }, () => {
  const result = check({ repoRoot: REPO_ROOT })
  assert.deepEqual(result.violations, [], result.violations.map((item) => item.message).join('\n'))
})
