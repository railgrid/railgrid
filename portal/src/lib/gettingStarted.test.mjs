import assert from 'node:assert/strict'
import test from 'node:test'
import { gettingStartedReadState } from './gettingStarted.ts'

const readyRead = {
  hasScope: true,
  catalogLoaded: true,
  catalogLoading: false,
  catalogMatchesOrganization: true,
  catalogError: false,
  bindingsMatchScope: true,
  bindingsState: 'ready',
  bindingsStale: false,
}

test('provider status is ready only for fresh catalog and bindings in the active scope', () => {
  assert.equal(gettingStartedReadState(readyRead), 'ready')
  assert.equal(gettingStartedReadState({ ...readyRead, bindingsMatchScope: false }), 'checking')
  assert.equal(gettingStartedReadState({ ...readyRead, bindingsState: 'loading', bindingsStale: true }), 'checking')
  assert.equal(gettingStartedReadState({ ...readyRead, catalogMatchesOrganization: false }), 'checking')
  assert.equal(gettingStartedReadState({ ...readyRead, bindingsState: 'error', bindingsStale: true }), 'error')
  assert.equal(gettingStartedReadState({ ...readyRead, catalogError: true }), 'error')
})

test('missing scope is actionable, even with old errors', () => {
  assert.equal(gettingStartedReadState({ ...readyRead, hasScope: false, catalogError: true }), 'unscoped')
})
