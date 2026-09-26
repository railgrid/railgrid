#!/usr/bin/env node

/**
 * Generates config/kcp/permissionclaimpolicy.yaml from the provider manifests.
 *
 * Every cross-provider relationship on this platform is already declared once,
 * in the declaring provider's manifest.yaml:
 *
 *   spec.requires[] = {provider, group, resources[]}
 *
 * Those declarations already drive tenant consent at Enable, the dependency
 * ordering the hub enforces, and the claims on the provider's own APIExport.
 * This generator makes them drive the kcp policy too, so there is exactly ONE
 * place a new edge is written down: a `requires` entry nobody whitelisted
 * becomes a `--check` failure rather than a runtime 403 nobody can explain.
 *
 * Only an entry that names a `provider` is a cross-provider claim. A
 * requirement with no provider is a platform builtin -- authorization.k8s.io,
 * authentication.k8s.io, the core group -- which no provider exports, which
 * nothing has to enable first, and which therefore needs no policy rule
 * admitting it.
 *
 * The output is a kcp `PermissionClaimPolicy` (group admin.kcp.io, see
 * kcp-dev/kcp#4385). Its spec has two fields:
 *
 *   claims[]    per CLAIMER api group, the api groups an APIExport exporting
 *               that claimer group may claim with an empty identityHash.
 *   providers[] the subjects allowed to export any group the policy reserves
 *               (a group is reserved as soon as it appears anywhere in claims,
 *               as claimer or as claimed).
 *
 * Two inputs, from two different files:
 *
 *   the CLAIMED groups  manifest.yaml spec.requires[].group, for the entries
 *                       that name a provider.
 *   the CLAIMER group   NOT reliably in the manifest. A provider's manifest
 *                       names its APIExport (spec.export.name) and the
 *                       apiVersion of each resource it hangs a verb or an action
 *                       off, but a resource with neither needs no entry at all,
 *                       so the manifest is not the full list of api groups that
 *                       export serves -- and the export name differs from the
 *                       group for most providers
 *                       (`edges.providers.railgrid.ai` exports
 *                       `edges.railgrid.ai`). The one authoritative answer is
 *                       spec.resources[].group on the export itself -- the same
 *                       projection pkg/hub/providers.APIExportGroups makes off
 *                       the live object. Here it is read off the GENERATED
 *                       export, providers/<name>/config/kcp/apiexport-<export
 *                       name>.yaml. A provider that requires another provider's
 *                       group but whose own exported group cannot be read is an
 *                       error, never a guess: guessing it wrong whitelists the
 *                       wrong claimer.
 *
 * Provider discovery matches hack/verify-provider-contract.mjs (a directory
 * under providers/ with a manifest.yaml), plus the same optional external
 * provider tree the Tiltfile takes: --external-providers-dir DIR, or
 * RAILGRID_EXTERNAL_PROVIDERS_DIR, scanned as DIR/providers/*​/manifest.yaml.
 *
 * --check regenerates in memory and compares against the committed file. It is
 * deliberately not a byte comparison: CI has no checkout of the external
 * provider repo, so a rule whose claimer group belongs to NO scanned provider
 * is left alone and reported as unscanned. Every rule whose claimer IS a
 * scanned provider's exported group is compared exactly, in both directions, so
 * an in-tree edge can neither go missing nor rot after the provider drops it.
 * Writing has the mirror guard: it refuses to drop rules it did not scan unless
 * --drop-unscanned says so.
 *
 * Nothing here depends on kcp serving admin.kcp.io. The file is data; the hub
 * applies it only when discovery says the API exists
 * (pkg/hub/bootstrap/permissionclaimpolicy.go).
 */

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

import { parseYAML } from './verify-provider-contract.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')

/** Where the generated policy is committed, relative to the repo root. */
export const POLICY_PATH = 'config/kcp/permissionclaimpolicy.yaml'

/** The generator's own path, as the header comment names it. */
export const GENERATOR = 'hack/generate-permission-claim-policy.mjs'

export const POLICY_API_VERSION = 'admin.kcp.io/v1alpha1'
export const POLICY_KIND = 'PermissionClaimPolicy'

/**
 * The policy object's name. One installation-wide policy covers the whole
 * platform: the claims are a single graph, and splitting it per provider would
 * let two objects reserve the same group with different provider subjects.
 */
export const POLICY_NAME = 'railgrid'

/**
 * spec.providers: the subjects allowed to export a reserved group.
 *
 * Every *.railgrid.ai API group in this policy is exported by a provider, and
 * every provider's APIExport is applied by the provider itself: the hub gives
 * each provider its own workspace (root:railgrid:providers:<name>, see
 * pkg/hub/providers/provision.go providersParentWorkspace) containing exactly
 * one ServiceAccount, default/provider (ProviderSANamespace/ProviderSAName),
 * mints that SA's kubeconfig, and the provider's `init`
 * (provider-sdk/install.ApplyAPIExport) applies the generated APIExport with
 * it. kcp reports that request as ProviderSAUsername, below -- the same string
 * the heartbeat authenticator pins on (pkg/hub/providers/heartbeat_auth.go) and
 * the same one hack/tilt/platform.py in the external provider repo hands to an
 * out-of-tree provider.
 *
 * So the subject is established, not invented. What is NOT settled is how
 * PRECISE it is: a plain ServiceAccount username is logical-cluster-scoped in
 * kcp -- the same string names a different principal in every workspace -- so
 * this subject also matches a `default/provider` ServiceAccount a tenant makes
 * in their own workspace. kcp's disambiguated form,
 * system:kcp:serviceaccount:{cluster}:{ns}:{name} (pkg/util/identity), cannot
 * be written here because {cluster} is the logical cluster id kcp assigns at
 * runtime, not the workspace path. Narrowing this is a question for the kcp
 * side; see the TODO in the emitted header.
 */
export const PROVIDER_SUBJECTS = Object.freeze([
  Object.freeze({ kind: 'User', name: 'system:serviceaccount:default:provider' }),
])

export const CHECKS = Object.freeze({
  MISSING_RULE: 'missing-rule',
  STALE_RULE: 'stale-rule',
  DRIFTED_RULE: 'drifted-rule',
  DRIFTED_HEADER: 'drifted-header',
  DRIFTED_SUBJECTS: 'drifted-subjects',
  MISSING_FILE: 'missing-file',
})

class PolicyError extends Error {}

function fail(message) {
  throw new PolicyError(message)
}

// ---------------------------------------------------------------------------
// Provider discovery
// ---------------------------------------------------------------------------

/**
 * The provider directories to scan: providers/<name>/ with a manifest.yaml in
 * the repo, and the same layout under an external providers checkout. The
 * external tree is the repo root the Tiltfile is pointed at (`../providers`),
 * so its providers live at DIR/providers/<name>/, exactly as in-tree.
 */
export function discoverProviders({ repoRoot, externalDir }) {
  const found = []
  const scan = (root, source) => {
    const providersRoot = path.join(root, 'providers')
    if (!fs.existsSync(providersRoot) || !fs.statSync(providersRoot).isDirectory()) return
    for (const name of fs.readdirSync(providersRoot).sort()) {
      const dir = path.join(providersRoot, name)
      if (!fs.existsSync(path.join(dir, 'manifest.yaml'))) continue
      // The label is where the header records this manifest came from. It must
      // not carry the external checkout's absolute location: that varies per
      // machine and would churn the committed file.
      found.push({ name, dir, source, label: `${source === 'external' ? 'external:' : ''}providers/${name}` })
    }
  }
  scan(repoRoot, 'in-tree')
  if (externalDir) {
    if (!fs.existsSync(externalDir)) fail(`--external-providers-dir ${externalDir} does not exist`)
    const providersRoot = path.join(externalDir, 'providers')
    if (!fs.existsSync(providersRoot)) fail(`--external-providers-dir ${externalDir} has no providers/ directory`)
    scan(externalDir, 'external')
  }
  // A provider name may appear in both trees only by mistake; the two would
  // declare different edges under one claimer and the last one would win.
  const seen = new Map()
  for (const provider of found) {
    const previous = seen.get(provider.name)
    if (previous) fail(`provider ${provider.name} is defined twice: ${previous.dir} and ${provider.dir}`)
    seen.set(provider.name, provider)
  }
  return found
}

function catalogEntrySpec(file) {
  let documents
  try {
    documents = parseYAML(fs.readFileSync(file, 'utf8'))
  } catch (error) {
    fail(`${file} is unreadable: ${error.message}`)
  }
  const entry = documents.find((document) => document && document.kind === 'CatalogEntry')
  if (!entry) fail(`${file} has no kind: CatalogEntry document`)
  if (!entry.spec || typeof entry.spec !== 'object') fail(`${file} CatalogEntry has no spec`)
  return entry.spec
}

/**
 * The api groups a provider's GENERATED APIExport serves, off
 * spec.resources[].group. Empty is a legitimate answer for a provider that
 * mints its resources at runtime (the infrastructure provider ships
 * `resources: []`); it only becomes an error when such a provider also requires
 * another provider's group, because then its claimer group is load-bearing.
 */
export function exportedGroups(provider) {
  const spec = catalogEntrySpec(path.join(provider.dir, 'manifest.yaml'))
  const exportName = spec?.export?.name
  if (typeof exportName !== 'string' || !exportName) {
    return { groups: [], why: `${provider.label}/manifest.yaml declares no spec.export.name` }
  }
  const exportPath = path.join(provider.dir, 'config', 'kcp', `apiexport-${exportName}.yaml`)
  if (!fs.existsSync(exportPath)) {
    return { groups: [], why: `no generated APIExport at ${provider.label}/config/kcp/apiexport-${exportName}.yaml; run make codegen-${provider.name}-provider` }
  }
  let documents
  try {
    documents = parseYAML(fs.readFileSync(exportPath, 'utf8'))
  } catch (error) {
    return { groups: [], why: `${provider.label}/config/kcp/apiexport-${exportName}.yaml is unreadable: ${error.message}` }
  }
  const exported = documents.find((document) => document && document.kind === 'APIExport')
  if (!exported) {
    return { groups: [], why: `${provider.label}/config/kcp/apiexport-${exportName}.yaml has no kind: APIExport document` }
  }
  const resources = Array.isArray(exported.spec?.resources) ? exported.spec.resources : []
  const groups = [...new Set(resources.map((resource) => String(resource?.group ?? '')).filter(Boolean))].sort()
  if (!groups.length) {
    return { groups: [], why: `${provider.label}/config/kcp/apiexport-${exportName}.yaml serves no spec.resources[].group, so the api group it exports cannot be determined` }
  }
  return { groups, why: null }
}

/**
 * The api groups a provider requires from ANOTHER PROVIDER, off
 * spec.requires[].group, in manifest order.
 *
 * Only the entries that name a `provider` count. A requirement with no provider
 * is a platform builtin (authorization.k8s.io, authentication.k8s.io, the core
 * group): nobody exports it, so no policy rule admits a claim on it and one
 * here would reserve a Kubernetes builtin group for the platform's providers.
 */
export function requiredGroups(provider) {
  const spec = catalogEntrySpec(path.join(provider.dir, 'manifest.yaml'))
  const requirements = Array.isArray(spec?.requires) ? spec.requires : []
  const groups = []
  for (const requirement of requirements) {
    const owner = String(requirement?.provider ?? '').trim()
    if (!owner) continue
    const group = String(requirement?.group ?? '').trim()
    if (!group) {
      fail(`${provider.label}/manifest.yaml spec.requires[] names provider ${owner} with no group; a requirement on a provider must name the api group that provider serves`)
    }
    groups.push(group)
  }
  return groups
}

// ---------------------------------------------------------------------------
// Generation
// ---------------------------------------------------------------------------

/**
 * Builds the policy from the scanned providers.
 *
 * Returns the policy object, its YAML, and the scan: `claimerGroups` is every
 * api group any scanned provider exports -- including providers that compose
 * nothing -- which is what lets --check tell a stale in-tree rule from a rule
 * that belongs to a provider outside this checkout.
 */
export function generate(options = {}) {
  const repoRoot = path.resolve(options.repoRoot ?? REPO_ROOT)
  const externalDir = options.externalDir ? path.resolve(options.externalDir) : null
  const providers = discoverProviders({ repoRoot, externalDir })

  const claimerGroups = new Set()
  const rulesByClaimer = new Map()
  let relationships = 0

  for (const provider of providers) {
    const required = requiredGroups(provider)
    const { groups, why } = exportedGroups(provider)
    for (const group of groups) claimerGroups.add(group)
    if (!required.length) continue
    relationships += required.length
    // Only now is the claimer group load-bearing, so only now is failing to
    // derive it fatal.
    if (!groups.length) {
      fail(`${provider.name} requires ${required.length} api group(s) from other providers but the api group it exports is unknown: ${why}`)
    }
    for (const claimer of groups) {
      const claimed = rulesByClaimer.get(claimer) ?? new Set()
      // A provider claiming its own group needs no whitelisting: an APIExport
      // always has its own identity.
      for (const group of required) if (group !== claimer) claimed.add(group)
      rulesByClaimer.set(claimer, claimed)
    }
  }

  const claims = [...rulesByClaimer.entries()]
    .filter(([, groups]) => groups.size)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([claimer, groups]) => ({ claimer, groups: [...groups].sort((a, b) => a.localeCompare(b)) }))

  const policy = {
    apiVersion: POLICY_API_VERSION,
    kind: POLICY_KIND,
    metadata: { name: POLICY_NAME },
    spec: {
      providers: PROVIDER_SUBJECTS.map((subject) => ({ ...subject })),
      claims,
    },
  }

  return {
    policy,
    yaml: renderPolicy(policy, { providers, relationships }),
    providers,
    relationships,
    claimerGroups,
    externalDir,
    repoRoot,
  }
}

function header({ providers, relationships }) {
  const scanned = providers.map((provider) => `#   ${provider.label}`).join('\n')
  return `# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# GENERATED FILE — DO NOT EDIT.
#
# Written by ${GENERATOR} from the provider
# manifests. Regenerate with:
#   make permission-claim-policy [EXTERNAL_PROVIDERS_DIR=../providers]
# \`make verify-provider-contract\` re-runs it with --check, so a new
# spec.requires[] entry that nobody whitelisted fails there.
#
# spec.claims[].groups  comes from each provider's manifest.yaml
#                       spec.requires[].group, for the entries that name a
#                       provider. A requirement with no provider is a platform
#                       builtin (authorization.k8s.io, the core group): no
#                       provider exports it, so no rule here admits it.
# spec.claims[].claimer comes from spec.resources[].group on that provider's
#                       generated APIExport (config/kcp/apiexport-*.yaml) — the
#                       manifest names the export (spec.export.name), not every
#                       group it serves, and the two differ for most providers.
# spec.providers        is the identity the hub mints for every provider:
#                       ServiceAccount default/provider in the provider's own
#                       workspace root:railgrid:providers/<name> (see
#                       pkg/hub/providers/provision.go and heartbeat_auth.go
#                       ProviderSAUsername). Every *.railgrid.ai APIExport here
#                       is applied by that identity, from the provider's own
#                       \`init\`.
#
#                       TODO(permissionclaimpolicy): a plain ServiceAccount
#                       username is logical-cluster-scoped in kcp, so this
#                       subject also matches a default/provider ServiceAccount
#                       a tenant creates in their own workspace. kcp's
#                       disambiguated form
#                       system:kcp:serviceaccount:{cluster}:{ns}:{name} cannot
#                       be written statically ({cluster} is assigned at
#                       runtime). Confirm with kcp whether the policy should
#                       accept a cluster-qualified subject, or whether the
#                       admission check already scopes the match to the
#                       exporting workspace.
#
# Generated from ${providers.length} provider manifest(s), ${relationships} cross-provider requirement(s):
${scanned}
`
}

/** The policy, rendered in the same block style as the generated APIExports. */
export function renderPolicy(policy, scan) {
  const lines = []
  lines.push(`apiVersion: ${policy.apiVersion}`)
  lines.push(`kind: ${policy.kind}`)
  lines.push('metadata:')
  lines.push(`  name: ${policy.metadata.name}`)
  lines.push('spec:')
  lines.push('  claims:')
  for (const rule of policy.spec.claims) {
    lines.push(`  - claimer: ${rule.claimer}`)
    lines.push('    groups:')
    for (const group of rule.groups) lines.push(`    - ${group}`)
  }
  lines.push('  providers:')
  for (const subject of policy.spec.providers) {
    lines.push(`  - kind: ${subject.kind}`)
    lines.push(`    name: ${JSON.stringify(subject.name)}`)
  }
  return `${header(scan)}${lines.join('\n')}\n`
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

function violation(check, message) {
  return { check, message, path: POLICY_PATH }
}

function readCommitted(file) {
  const documents = parseYAML(fs.readFileSync(file, 'utf8'))
  const policy = documents.find((document) => document && document.kind === POLICY_KIND)
  if (!policy) fail(`${POLICY_PATH} has no kind: ${POLICY_KIND} document`)
  return policy
}

function ruleMap(policy) {
  const rules = Array.isArray(policy?.spec?.claims) ? policy.spec.claims : []
  const map = new Map()
  for (const rule of rules) {
    const claimer = String(rule?.claimer ?? '')
    if (!claimer) continue
    map.set(claimer, (Array.isArray(rule?.groups) ? rule.groups : []).map(String))
  }
  return map
}

function subjectKey(subjects) {
  return (Array.isArray(subjects) ? subjects : [])
    .map((subject) => `${subject?.kind ?? ''}:${subject?.name ?? ''}`)
    .sort()
    .join(' ')
}

/**
 * Compares the committed file with a fresh generation.
 *
 * A rule whose claimer is a group some scanned provider exports is compared
 * exactly, in both directions. A rule whose claimer no scanned provider exports
 * is reported as unscanned and left alone: that is a provider in a checkout CI
 * does not have (run with --external-providers-dir to check those too).
 */
export function check(options = {}) {
  const result = generate(options)
  const file = path.join(result.repoRoot, POLICY_PATH)
  if (!fs.existsSync(file)) {
    return { ...result, violations: [violation(CHECKS.MISSING_FILE, `is missing; run make permission-claim-policy`)], unscanned: [] }
  }
  const committed = readCommitted(file)
  const violations = []

  if (committed.apiVersion !== POLICY_API_VERSION || committed.metadata?.name !== POLICY_NAME) {
    violations.push(violation(CHECKS.DRIFTED_HEADER, `declares ${committed.apiVersion} ${JSON.stringify(committed.metadata?.name ?? null)}, want ${POLICY_API_VERSION} ${JSON.stringify(POLICY_NAME)}`))
  }
  if (subjectKey(committed.spec?.providers) !== subjectKey(result.policy.spec.providers)) {
    violations.push(violation(CHECKS.DRIFTED_SUBJECTS, `spec.providers is ${JSON.stringify(committed.spec?.providers ?? null)}, want ${JSON.stringify(result.policy.spec.providers)}`))
  }

  const want = ruleMap(result.policy)
  const have = ruleMap(committed)
  const unscanned = []

  for (const [claimer, groups] of want) {
    if (!have.has(claimer)) {
      violations.push(violation(CHECKS.MISSING_RULE, `has no rule for claimer ${claimer}; a spec.requires entry declares ${groups.join(', ')} but nothing whitelists it. Run make permission-claim-policy`))
      continue
    }
    const current = have.get(claimer)
    if (current.join(',') !== groups.join(',')) {
      violations.push(violation(CHECKS.DRIFTED_RULE, `claimer ${claimer} whitelists [${current.join(' ')}] but the manifests require [${groups.join(' ')}]. Run make permission-claim-policy`))
    }
  }
  for (const claimer of have.keys()) {
    if (want.has(claimer)) continue
    if (result.claimerGroups.has(claimer)) {
      violations.push(violation(CHECKS.STALE_RULE, `claimer ${claimer} is exported by a scanned provider that requires nothing from another provider; the rule is stale. Run make permission-claim-policy`))
      continue
    }
    unscanned.push(claimer)
  }

  return { ...result, violations, unscanned }
}

/** Writes the policy, refusing to silently drop rules this scan could not see. */
export function write(options = {}) {
  const result = generate(options)
  const file = path.join(result.repoRoot, POLICY_PATH)
  if (fs.existsSync(file) && !options.dropUnscanned) {
    const have = ruleMap(readCommitted(file))
    const want = ruleMap(result.policy)
    const dropped = [...have.keys()].filter((claimer) => !want.has(claimer) && !result.claimerGroups.has(claimer))
    if (dropped.length) {
      fail(`writing would drop ${dropped.length} rule(s) for claimer(s) no scanned provider exports (${dropped.join(', ')}). They come from a provider tree this run did not scan: re-run with --external-providers-dir, or pass --drop-unscanned to remove them on purpose`)
    }
  }
  const existing = fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : null
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, result.yaml)
  return { ...result, file, changed: existing !== result.yaml }
}

export function formatViolation(item) {
  return `${item.path} [${item.check}] ${item.message}`
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const options = {}
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '--check') options.check = true
    else if (arg === '--json') options.json = true
    else if (arg === '--drop-unscanned') options.dropUnscanned = true
    else if (arg === '--external-providers-dir') options.externalDir = argv[++index]
    else if (arg.startsWith('--external-providers-dir=')) options.externalDir = arg.slice('--external-providers-dir='.length)
    else if (arg === '--repo-root') options.repoRoot = argv[++index]
    else if (arg === '--help' || arg === '-h') options.help = true
    else fail(`unknown argument ${arg}`)
  }
  // Same precedence as the Tiltfile: the flag wins, the env var is the fallback,
  // and an empty value means "no external tree".
  if (!options.externalDir) options.externalDir = process.env.RAILGRID_EXTERNAL_PROVIDERS_DIR || ''
  if (!options.externalDir) options.externalDir = null
  return options
}

function usage() {
  return [
    `Usage: node ${GENERATOR} [options]`,
    '',
    `Generates ${POLICY_PATH} from every providers/*/manifest.yaml:`,
    '  spec.requires[].group (provider entries)  -> the groups a provider may claim',
    '  the provider\'s generated APIExport        -> the claimer group it exports',
    '',
    'Options:',
    '  --check                         Fail if the committed file is stale',
    '  --external-providers-dir DIR    Also scan DIR/providers/*/manifest.yaml',
    '                                  (env RAILGRID_EXTERNAL_PROVIDERS_DIR)',
    '  --drop-unscanned                When writing, remove rules for claimers no',
    '                                  scanned provider exports',
    '  --repo-root PATH                Scan another checkout',
    '  --json                          Machine-readable output',
  ].join('\n')
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
    if (options.help) {
      console.log(usage())
      return
    }
    if (options.check) {
      const result = check(options)
      if (options.json) {
        console.log(JSON.stringify({ violations: result.violations, unscanned: result.unscanned, providers: result.providers.map((p) => p.label), relationships: result.relationships, claims: result.policy.spec.claims }, null, 2))
      } else {
        console.log(`Permission claim policy scanned ${result.providers.length} provider(s), ${result.relationships} cross-provider requirement(s): ${result.violations.length} violation(s), ${result.unscanned.length} unscanned rule(s) left alone.`)
        for (const item of result.violations) console.log(formatViolation(item))
        if (result.unscanned.length) console.log(`Permission claim policy unscanned claimers (no provider in this checkout exports them): ${result.unscanned.join(' ')}`)
      }
      if (result.violations.length) process.exitCode = 1
      return
    }
    const result = write(options)
    if (options.json) {
      console.log(JSON.stringify({ file: POLICY_PATH, changed: result.changed, providers: result.providers.map((p) => p.label), claims: result.policy.spec.claims }, null, 2))
    } else {
      console.log(`Permission claim policy wrote ${POLICY_PATH} (${result.changed ? 'changed' : 'unchanged'}) from ${result.providers.length} provider(s): ${result.policy.spec.claims.length} claimer(s), ${result.relationships} cross-provider requirement(s).`)
    }
  } catch (error) {
    if (!(error instanceof PolicyError)) throw error
    console.error(`Permission claim policy configuration error: ${error.message}`)
    process.exitCode = 2
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))) main()
