import fs from 'node:fs'
import { expect, it } from 'vitest'

const app = fs.readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const connectionCollection = fs.readFileSync(new URL('./views/ConnectionsView.vue', import.meta.url), 'utf8')
const connectionCreate = fs.readFileSync(new URL('./views/ConnectionCreateView.vue', import.meta.url), 'utf8')
const repositoryCollection = fs.readFileSync(new URL('./views/RepositoriesView.vue', import.meta.url), 'utf8')
const repositoryCreate = fs.readFileSync(new URL('./views/RepositoryCreateView.vue', import.meta.url), 'utf8')

it('Code collections stay cached across routed create and detail surfaces', () => {
  expect(app).toMatch(/<KeepAlive :max="1">[\s\S]*<ConnectionsView/)
  expect(app).toMatch(/<KeepAlive :max="1">[\s\S]*<RepositoriesView/)
  expect(app).toMatch(/!route\.create && !route\.connection/)
  expect(app).toMatch(/!route\.create && !route\.repo/)
  expect(app).toMatch(/:key="`\$\{contextGeneration\}:connections`"/)
  expect(app).toMatch(/:key="`\$\{contextGeneration\}:repositories`"/)
})

it('GitHub OAuth configuration failures settle and expose retry actions', () => {
  for (const source of [connectionCollection, connectionCreate]) {
    expect(source).toMatch(/async function loadOAuthConfig\(\): Promise<void>/)
    expect(source).toMatch(/catch \(e\)/)
    expect(source).toMatch(/oauthLoaded\.value = true/)
    expect(source).toMatch(/@click="loadOAuthConfig"/)
    expect(source).toMatch(/Retry GitHub sign-in/)
  }
})

it('create routes fence every async continuation on the synchronous authority generation', () => {
  expect(app).toMatch(/provide\(contextGenerationKey, contextGeneration\)/)
  expect(app).toMatch(/flush: 'sync'/)
  for (const source of [connectionCreate, repositoryCreate]) {
    expect(source).toMatch(/inject\(contextGenerationKey, ref\(0\)\)/)
    expect(source).toMatch(/function isCurrentMutation\(/)
    expect(source).toMatch(/if \(!isCurrentMutation\(generation, expectedContext\)\) return/)
    expect(source).toMatch(/if \(isCurrentMutation\(generation, expectedContext\)\) formError\.value/)
    expect(source).toMatch(/if \(isCurrentMutation\(generation, expectedContext\)\) submitting\.value = false/)
  }
})

it('connection creation performs an authoritative collision check before connect', () => {
  expect(connectionCreate).toMatch(/const currentConnections = await api\.listConnections\(\)/)
  expect(connectionCreate).toMatch(/normalizeResourceName\(connection\.name\) === desiredName/)
  expect(connectionCreate).toMatch(/already exists\./)
  expect(connectionCreate).toMatch(/const created = await api\.connect\(payload\)/)
})

it('route cancellation cannot hide an in-flight create mutation', () => {
  for (const source of [connectionCreate, repositoryCreate]) {
    expect(source).toMatch(/function cancel\(\): void \{\s+if \(submitting\.value\) return/)
    expect(source).toMatch(/k-back-action" type="button" :disabled="submitting" @click="cancel"/)
    expect(source).toMatch(/type="button" :disabled="submitting" @click="cancel">Cancel/)
    expect(source).not.toMatch(/@click(?:\.prevent)?="emit\('cancel'\)"/)
  }
  expect(connectionCreate).toMatch(/if \(oauthBusy\.value\) clearOAuthWait\(\)/)
  expect(connectionCreate).toMatch(/if \(popup && !popup\.closed\) popup\.close\(\)/)
})

it('primary create forms expose native constraints, field errors, focus recovery, and progress', () => {
  for (const source of [connectionCreate, repositoryCreate]) {
    expect(source).toContain('fieldErrors')
    expect(source).toContain('function focusField(')
    expect(source).toMatch(/required aria-required="true"/)
    expect(source).toContain(':aria-invalid=')
    expect(source).toContain(':aria-describedby=')
    expect(source).toMatch(/role="status" aria-live="polite"/)
    expect(source).toContain(':aria-busy="submitting"')
  }
  expect(connectionCreate).toContain('autocomplete="new-password"')
  expect(repositoryCreate).toContain('code-repository-connection-error')
})

it('authoritative empty collections use the shared first-run journey', () => {
  for (const source of [connectionCollection, repositoryCollection]) {
    expect(source).toMatch(/import FirstRunGuide from '\.\.\/portalkit\/FirstRunGuide\.vue'/)
    expect(source).toMatch(/const showFirstRun = computed\(\(\) => loaded\.value/)
    expect(source).toMatch(/<FirstRunGuide[\s\S]*v-if="showFirstRun"/)
    expect(source).toMatch(/<ResourceTable[\s\S]*v-else/)
    expect(source).toContain('CODE_JOURNEY_STEPS')
    expect(source).toContain('journey-label="Code setup path"')
  }
  expect(connectionCollection).toMatch(/oauthLoaded\.value[\s\S]*!error\.value[\s\S]*connections\.value\.length === 0/)
  expect(connectionCollection).not.toMatch(/showFirstRun = computed\([^\n]*oauthError/)
  expect(repositoryCollection).toMatch(/repositoryCollectionIsAuthoritativelyEmpty/)
  expect(repositoryCollection).toMatch(/&& repositoryCollectionEmpty\.value/)
})

it('repository first-run state exposes its connection prerequisite action', () => {
  expect(repositoryCollection).toMatch(/function handleFirstRun\(action: CodeJourneyAction\)/)
  expect(repositoryCollection).toMatch(/action === 'create-connection'\) emit\('create-connection'\)/)
  expect(app).toMatch(/@create-connection="startRepositoryConnection"/)
  expect(app).toMatch(/prerequisiteReturnPath\.value = 'create\/repository'[\s\S]*writeCodeReturnIntent/)
  expect(app).toMatch(/prerequisiteExpectedPath\.value === activePath[\s\S]*prerequisiteTenantKey\.value === tenantKey/)
  expect(app).toMatch(/writeCodeReturnIntent\(journeyStorage, tenantKey, 'create\/repository', expectedPath\)/)
  expect(app).toMatch(/dispatchNavigation\(success \? returnPath : 'repositories', \{ replace: true \}\)/)
  expect(repositoryCreate).toMatch(/Add an active connection first[\s\S]*Create connection/)
  expect(repositoryCreate).toMatch(/:disabled="submitting \|\| !connectionChoices\.length"/)
})

it('connection and repository forms use the shared live guidance rail', () => {
  for (const source of [connectionCreate, repositoryCreate]) {
    expect(source).toMatch(/import CreateGuidance from '\.\.\/portalkit\/CreateGuidance\.vue'/)
    expect(source).toContain('k-create-surface--guided')
    expect(source).toContain('k-create-body--guided')
    expect(source).toContain('k-create-fields')
    expect(source).toMatch(/<CreateGuidance[\s\S]*:values="guidanceValues"[\s\S]*:next-steps="guidanceNextSteps"/)
  }
  expect(connectionCreate).toMatch(/label: 'Credential'[\s\S]*stored as a Secret/)
  expect(repositoryCreate).toMatch(/label: 'GitHub repository'/)
  expect(repositoryCreate).toMatch(/label: 'Initial content'/)
})
