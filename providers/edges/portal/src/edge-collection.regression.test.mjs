import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (file) => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')
const app = readSource('App.vue')
const collection = readSource('EdgeCollection.vue')

describe('Edges collection route-state regression', () => {
  it('keeps the collection instance alive and revalidates when returning from routes', () => {
    const cachedCollection = app.match(/<KeepAlive>\s*<EdgeCollection[\s\S]*?<\/KeepAlive>/)?.[0]
    expect(cachedCollection).toBeTruthy()
    expect(cachedCollection).toMatch(/:key="`edges:\$\{contextGeneration\}`"/)
    expect(cachedCollection).toMatch(/@activated="onEdgeCollectionActivated"/)
    expect(cachedCollection).toMatch(/@refresh="refresh"/)
    expect(collection).toMatch(/onActivated\(\(\) => emit\('activated'\)\)/)
    expect(collection).toMatch(/<ResourceTable[\s\S]*searchable[\s\S]*paginated/)
    expect(app).toMatch(/function onEdgeCollectionActivated\(\): void \{[\s\S]*if \(firstLoadDone\.value\) void refresh\('foreground'\)/)
  })

  it('replaces the detail route on Back so browser Back does not reopen it', () => {
    expect(app).toMatch(/<Detail[\s\S]*@back="navigate\('', true\)"/)
  })

  it('replaces prerequisite entry so returning does not duplicate the create route in history', () => {
    expect(app).toMatch(/function connectEdgeFrom\(successPath: string, options: EdgeConnectOptions = \{\}\): void \{[\s\S]*navigate\(edgeConnectPath\(successPath, options\), true\)/)
    expect(app).toMatch(/@connect-edge="connectEdgeFrom\(workloadDeployPath\(route\.deploy\.mode, route\.deploy\.app\), \{ requiredType: 'kubernetes' \}\)"/)
    expect(app).toMatch(/@connect-edge="connectEdgeFrom\('create\/service', \{ cancelPath: 'services' \}\)"/)
    expect(app).toMatch(/@connect-edge="connectEdgeFrom\('deploy\/workload\/manual', \{ cancelPath: 'workloads', requiredType: 'kubernetes' \}\)"/)
    expect(app).toMatch(/:required-type="route\.connect\.requiredType"/)
  })

  it('keeps an authoritative empty collection on an instructive first-run surface', () => {
    expect(app).not.toMatch(/edges\.value\.length === 0[\s\S]*navigate\('connect\/edge'/)
    expect(collection).toMatch(/import FirstRunGuide from '\.\/portalkit\/FirstRunGuide\.vue'/)
    expect(collection).toMatch(/const hasActiveTableFilters = computed\(/)
    expect(collection).toMatch(/const showFirstRun = computed\(\(\) => props\.loaded && !props\.error && props\.edges\.length === 0 && !hasActiveTableFilters\.value\)/)
    expect(collection).toMatch(/v-model:query="tableQuery"/)
    expect(collection).toMatch(/v-model:filter-values="tableFilters"/)
    expect(collection).toMatch(/<FirstRunGuide[\s\S]*title="Connect your first edge"[\s\S]*primary-label="Connect edge"/)
    expect(collection).toMatch(/<ResourceTable\s+v-else/)
  })
})
