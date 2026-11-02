// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = await readFile(new URL('./ProvidersPage.vue', import.meta.url), 'utf8')
const providerStoreSource = await readFile(new URL('../stores/providers.ts', import.meta.url), 'utf8')
const orgProviderTypes = await readFile(new URL('../stores/orgProviders.ts', import.meta.url), 'utf8')

test('self-hosting Edges prerequisite uses catalog-owned availability states', () => {
  assert.match(source, /const edgesProvider = computed\(\(\) => providers\.byName\('edges'\)\)/)
  assert.match(source, /if \(!edges\) return 'absent'/)
  assert.match(source, /return edges\.ready && !!edges\.serving\?\.ui \? 'ready' : 'unready'/)
})

test('only a ready Edges portal receives the provider deep link', () => {
  assert.match(
    source,
    /<router-link\s+v-else-if="edgesSelfHostState === 'ready'"\s+:to="scopePath\('\/providers\/edges'\)"/,
  )
  assert.match(source, /v-else-if="edgesSelfHostState === 'unready'"/)
  assert.match(source, /v-else-if="edgesSelfHostState === 'absent'"/)
  assert.match(source, /View Edges in catalog/)
  assert.match(source, /Edges is not installed in this catalog\./)
})

test('a failed install-target check is visible and retryable', () => {
  assert.match(source, /v-if="orgProviders\.installTargetsError"/)
  assert.match(source, /@click="orgProviders\.loadInstallTargets"/)
  assert.match(source, /Retry check/)
})

test('unready Edges returns to a filtered catalog instead of navigating to a dead route', () => {
  assert.match(
    source,
    /function showEdgesCatalogEntry\(\) \{\s+setTab\('catalog'\)\s+selectedCategory\.value = null\s+search\.value = 'edges'\s+\}/,
  )
  assert.match(source, /@click="showEdgesCatalogEntry"/)
})

test('ready rows have no Advanced or Installation details action', () => {
  assert.doesNotMatch(source, /<ActionMenu|selfHostedAdvancedItems|onSelfHostedAdvancedAction/)
  assert.doesNotMatch(source, />\s*Installation details\s*<\/button>/)
})

test('Ready returns to the Self-Hosting list and legacy manage links normalize without fetching credentials', () => {
  const normalize = source.slice(source.indexOf('function normalizeProviderRoute()'), source.indexOf('\nwatch(', source.indexOf('function normalizeProviderRoute()')))
  assert.match(normalize, /if \(hasManage\)[\s\S]*patch\.tab = 'self-hosting'[\s\S]*patch\.manage = null[\s\S]*patch\.install = null[\s\S]*patch\.manual = null/)
  assert.doesNotMatch(normalize, /orgProviders\.instructions|showInstructions/)
  const finish = source.slice(source.indexOf('async function finishInstall('), source.indexOf('\n}', source.indexOf('async function finishInstall(')) + 2)
  assert.match(finish, /patchProviderQuery\(\{ tab: 'self-hosting', install: null, manage: null, manual: null \}, true\)/)
  assert.match(finish, /loadOrgProviderInventory\(\)/)
  assert.match(finish, /selfHostedHeading\.value\?\.focus\(\)/)
  assert.match(finish, /const targetScope = dialogScope\.value[\s\S]*await patchProviderQuery[\s\S]*if \(dialogScope\.value !== targetScope\) return/)
  assert.doesNotMatch(finish, /manage: name|instructions\(/)
  assert.doesNotMatch(source, /Self-hosted provider management|Use in a workspace|Close management|showManagedProvider|managedBinding/)
  const selfHosting = source.slice(source.indexOf('<!-- ===== Self-Hosting tab ===== -->'), source.indexOf('<!-- ===== Catalog tab ===== -->'))
  assert.doesNotMatch(selfHosting, /openEnableDialog|onDisable\(/)
  assert.match(finish, /Workspace access is managed in Catalog\./)
  assert.match(source, /@click="openEnableDialog\(p\)"/)
  assert.match(source, /@click="onDisable\(p\)"/)
})

test('the org-shadow card reports the actual current workspace binding copy', () => {
  assert.match(source, /function bindingProjectionIsCurrent\(\): boolean \{[\s\S]*bindingsLoadState === 'ready' && !providers\.bindingsStale[\s\S]*bindingsOrgUUID === tenant\.orgUUID\s*&&\s*providers\.bindingsWorkspace === tenant\.workspaceUUID[\s\S]*bindingsRequestOrgUUID === tenant\.orgUUID\s*&&\s*providers\.bindingsRequestWorkspaceUUID === tenant\.workspaceUUID/)
  assert.match(source, /const binding = currentWorkspaceBinding\(p\)[\s\S]*return binding\.selfHosted \? 'organization' : 'platform'/)
  assert.match(source, /copy === 'platform'\) return 'Enabled · platform copy'/)
  assert.match(source, /copy === 'organization'\) return 'Enabled · org copy'/)
  assert.match(source, /if \(!p\.ready\)[\s\S]*copy === 'platform'\) return 'Not ready · platform copy'[\s\S]*copy === 'organization'\) return 'Not ready · org copy'/)
  assert.match(source, /Your organization’s entry shadows the platform entry, but existing workspace bindings may still use either copy\./)
  assert.match(source, /workspaceProviderCopy\(p\) === 'organization'/, 'Open is offered only when the current workspace actually uses the org copy')
})

test('disabling an org-shadowed platform binding requires explicit confirmation and fresh binding identity', () => {
  const disable = source.slice(source.indexOf('async function onDisable('), source.indexOf('\nfunction dependencyNotice', source.indexOf('async function onDisable(')))
  assert.match(disable, /const bindingBefore = currentWorkspaceBinding\(p\)/)
  assert.match(disable, /const migrationConfirmRequired = selfHostedPlatformBinding\(p\)/)
  assert.match(disable, /const confirmed = await confirmDialog\(/)
  assert.match(disable, /title: `Disable the platform copy of \$\{providerDisplayName\(p\)\}\?`/)
  assert.match(disable, /Disabling removes that binding and can delete resources/)
  assert.match(disable, /use Enable on this Catalog entry to bind the workspace to your organization’s copy/)
  assert.match(disable, /if \(!confirmed \|\| !isCurrent\(\)\) return/)
  assert.match(disable, /await providers\.refreshBindings\(\)/)
  assert.match(disable, /bindingIdentity\(bindingAfter\) !== bindingIdentity\(bindingBefore!\)/)
  assert.match(disable, /await providers\.disable\(p\)/)
  assert.match(source, /:disabled="!!busy\[p\.name\] \|\| !!migrationConfirming\[p\.name\] \|\| !canDisableFromThisProjection\(p\)"/)
  assert.match(source, /function canDisableFromThisProjection\(p: ProviderDTO\): boolean/)
})

test('workspace removal shows operation-specific progress and blocks follow-up actions while deleting', () => {
  assert.match(source, /:aria-busy="!!selfHostRemoval\[p\.name\] \|\| p\.deleting \|\| undefined"/)
  assert.match(source, /selfHostRemoval\[p\.name\] === 'confirming' \? 'Confirming…' : selfHostRemoval\[p\.name\] \|\| p\.deleting \? 'Removing…' : 'Remove'/)
  assert.match(source, /v-if="!p\.ready && !p\.deleting"[\s\S]*?@click="startInstall\(p\)"[\s\S]*?Continue installation/)
  assert.match(source, /Removing the organization workspace and API registration\. The Helm release in your cluster is not removed\./)
  assert.match(source, /if \(!canManageSelfHostedCredentials\.value \|\| confirmingRemoval\.value \|\| selfHostRemoval\.value\[name\] \|\| orgProviders\.byName\[name\]\?\.deleting\) return/)
})

test('ready provider rows identify the recorded hosting edge, workspace, version, and health', () => {
  assert.match(orgProviderTypes, /hostingEdge\?: \{\s+name: string\s+workspace: string\s+workspaceDisplayName\?: string/)
  assert.match(source, /Hosting Edge: <strong[^>]*>\{\{ p\.hostingEdge\?\.name \|\| 'Unavailable' \}\}<\/strong>/)
  assert.match(source, /Workspace: \{\{ p\.hostingEdge\.workspaceDisplayName \|\| p\.hostingEdge\.workspace \}\}/)
  assert.match(source, /Version: \{\{ p\.installedVersion \|\| p\.version \|\| 'Not reported' \}\}/)
  assert.match(source, /Health: \{\{ p\.deleting \? 'Removing' : p\.ready \? 'Ready' : p\.registered \? 'Not ready' : 'Awaiting install' \}\}/)
  assert.match(source, /A newer chart is available\./)
  assert.match(source, /Manual upgrade instructions/)
})

test('confirmed self-host removal reloads the scoped catalog so the platform recipe becomes available again', () => {
  const completion = source.slice(
    source.indexOf('async function finishSelfHostRemoval('),
    source.indexOf('\nfunction scheduleSelfHostRemovalPoll', source.indexOf('async function finishSelfHostRemoval(')),
  )
  const removal = source.slice(
    source.indexOf('async function removeSelfHosted('),
    source.indexOf('\nfunction clearSelfHostRemoval', source.indexOf('async function removeSelfHosted(')),
  )
  const poll = source.slice(
    source.indexOf('function scheduleSelfHostRemovalPoll('),
    source.indexOf('\nfunction stopSelfHostRemovalPolling', source.indexOf('function scheduleSelfHostRemovalPoll(')),
  )
  const refresh = source.slice(
    source.indexOf('async function refreshSelfHostingCatalog('),
    source.indexOf('\n}', source.indexOf('async function refreshSelfHostingCatalog(')) + 2,
  )
  const manualRefresh = source.slice(
    source.indexOf('async function refreshOrgProviderInventory('),
    source.indexOf('\nasync function refreshSelfHostingCatalog', source.indexOf('async function refreshOrgProviderInventory(')),
  )
  const available = source.slice(
    source.indexOf('const availableToSelfHost = computed('),
    source.indexOf('\n)', source.indexOf('const availableToSelfHost = computed(')) + 2,
  )

  assert.match(completion, /targetOrgUUID: string \| null, targetScope: string/)
  assert.match(completion, /tenant\.orgUUID !== targetOrgUUID \|\| dialogScope\.value !== targetScope\) return/)
  assert.match(source, /import \{ toast \} from '@\/portalkit\/toast'/)
  assert.match(completion, /toast\('ok', `\$\{label\} was removed from self-hosting\.`, \{ scope: targetScope \}\)/)
  assert.doesNotMatch(completion, /selfHostNotice\.value/, 'removal success is not left as a persistent inline notice')
  assert.match(completion, /await refreshSelfHostingCatalog\(targetOrgUUID, targetScope\)/)
  assert.match(removal, /await finishSelfHostRemoval\(name, operation, targetOrgUUID, targetScope\)/)
  assert.match(poll, /await finishSelfHostRemoval\(name, operation, targetOrgUUID, targetScope\)/)
  assert.match(refresh, /tenant\.orgUUID !== targetOrgUUID \|\| dialogScope\.value !== targetScope\) return/)
  assert.match(refresh, /await providers\.load\(targetOrgUUID\)/)
  assert.match(manualRefresh, /Promise\.all\(\[[\s\S]*loadOrgProviderInventory\(\)[\s\S]*refreshSelfHostingCatalog\(targetOrgUUID, targetScope\)/)
  assert.match(providerStoreSource, /function isSelfManaged\(p: ProviderDTO\): boolean \{\s+return p\.scope === 'org'/)
  assert.match(providerStoreSource, /const selfHostable = computed<ProviderDTO\[]>\(\(\) =>[\s\S]*?\.filter\(\(p\) => p\.serving\?\.selfHosting\?\.supported && !isSelfManaged\(p\)\)/)
  assert.match(available, /providers\.selfHostable\.filter\(\(p\) => !inventoryCurrent\.value \|\| !orgProviders\.isSelfHosted\(p\.name\)\)/)

  // The removed org-scoped DTO is excluded from the platform recipe list.
  // A fresh catalog response with the platform DTO restores it to the list.
  const orgShadow = { name: 'kuery', scope: 'org', selfHosting: true }
  const platformProvider = { name: 'kuery', scope: 'platform', selfHosting: true }
  const registrations = new Set(['kuery'])
  const selfHostable = (items) => items.filter((provider) => provider.selfHosting && provider.scope !== 'org')
  const availableFrom = (items) => selfHostable(items).filter((provider) => !registrations.has(provider.name))
  assert.deepEqual(availableFrom([orgShadow]), [])
  registrations.delete('kuery')
  assert.deepEqual(availableFrom([platformProvider]), [platformProvider])
})
