import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createFixtureFetch } from './product-fetch.mjs'

test('stale refresh retains a realistic successful snapshot and then fails once', async () => {
  let time = 0
  const fetch = createFixtureFetch({ origin: 'http://localhost', staleRefresh: true, now: () => time })
  const path = '/clusters/review/apis/edges.railgrid.ai/v1alpha1/services'
  const first = await fetch(path)
  assert.equal(first.status, 200)
  assert.equal((await first.json()).items[0].metadata.name, 'home-assistant')
  time = 10_000
  const failed = await fetch(path)
  assert.equal(failed.status, 503)
  assert.match((await failed.json()).message, /could not be refreshed/)
  const retry = await fetch(path)
  assert.equal(retry.status, 200)
  assert.equal((await retry.json()).items[0].metadata.name, 'home-assistant')
})
