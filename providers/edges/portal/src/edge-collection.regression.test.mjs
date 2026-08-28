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
})
