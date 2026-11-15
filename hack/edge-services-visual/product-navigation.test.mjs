import { test } from 'node:test'
import assert from 'node:assert/strict'
import { installFixtureNavigation } from './product-navigation.mjs'

test('provider navigation updates context, preserves scenarios and restores history', () => {
  let url = new URL('http://localhost/product.html?scenario=empty-refresh')
  const listeners = {}
  const entries = []
  const element = { addEventListener: (name, cb) => { listeners[name] = cb } }
  const window = {
    get location() { return url },
    addEventListener: (name, cb) => { listeners[name] = cb },
    history: {
      pushState(_state, _title, next) { entries.push(url); url = new URL(next) },
      replaceState(_state, _title, next) { url = new URL(next) },
    },
  }
  const fetch = () => {}
  installFixtureNavigation({ element, initialPath: 'services', fixtureFetch: fetch, getTheme: () => 'light', window })
  assert.equal(element.railgridContext.subPath, 'services')
  assert.equal(element.railgridContext.fetch, fetch)
  listeners['railgrid-navigate']({ detail: { path: 'create/service' } })
  assert.equal(element.railgridContext.subPath, 'create/service')
  assert.equal(url.searchParams.get('scenario'), 'empty-refresh')
  listeners['railgrid-navigate']({ detail: { path: '', replace: true } })
  assert.equal(element.railgridContext.subPath, '')
  assert.equal(entries.length, 1)
  url = entries.pop()
  listeners.popstate()
  assert.equal(element.railgridContext.subPath, 'services')
})
