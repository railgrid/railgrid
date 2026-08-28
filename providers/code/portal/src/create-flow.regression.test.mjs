import fs from 'node:fs'
import { expect, it } from 'vitest'

const app = fs.readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const connectionCollection = fs.readFileSync(new URL('./views/ConnectionsView.vue', import.meta.url), 'utf8')
const connectionCreate = fs.readFileSync(new URL('./views/ConnectionCreateView.vue', import.meta.url), 'utf8')
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
