import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, nextTick, reactive, ref } from 'vue'

const dialogs = {
  member: { file: './AddMemberDialog.vue', mutation: 'add', draft: 'newUser', role: 'newRole' },
  serviceAccount: { file: './CreateServiceAccountDialog.vue', mutation: 'create', draft: 'name', role: 'role' },
}

function setup(kind) {
  const definition = dialogs[kind]
  const source = readFileSync(new URL(definition.file, import.meta.url), 'utf8')
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const code = ts.transpileModule(script.replace(/^import .*$/gm, ''), {
    compilerOptions: { target: ts.ScriptTarget.ES2020 },
  }).outputText
  let dispose, mount
  const state = { calls: [], emitted: [], nativeCloses: 0, opens: 0, result: async () => true }
  const suggestions = ref([])
  const props = reactive({
    scope: 'workspace', scopeName: 'Production', organizationName: 'Acme', workspaceName: 'Production',
    members: [{ user: 'existing', role: 'member' }], errorMessage: null,
    [definition.mutation]: async (value, role) => {
      state.calls.push({ value, role })
      return state.result()
    },
  })
  class HTMLElement {
    isConnected = true
    focuses = 0
    focus() { this.focuses++ }
  }
  const trigger = new HTMLElement()
  const extras = kind === 'member' ? ', onUserKeydown, suggestionsOpen, showSuggestions, activeSuggestion, suggestions' : ''
  const api = runInNewContext(`${code}\n({ submit, close, busy, error, dialogRef, draft: ${definition.draft}, role: ${definition.role}${extras} })`, {
    computed, nextTick, ref, Error, HTMLElement, document: { activeElement: trigger },
    useId: () => 'test-dialog', defineProps: () => props,
    defineEmits: () => event => state.emitted.push(event),
    useUserSuggestions: () => ({ suggestions }),
    onMounted: callback => { mount = callback },
    onBeforeUnmount: callback => { dispose = callback },
  })
  api.dialogRef.value = { showModal() { state.opens++ }, close() { state.nativeCloses++ } }
  mount()
  return { api, state, props, suggestions, trigger, dispose: () => dispose() }
}

for (const kind of Object.keys(dialogs)) {
  test(`${kind}: cancel opens no mutation and restores focus`, () => {
    const { api, state, trigger, dispose } = setup(kind)
    assert.equal(state.opens, 1)
    api.close()
    dispose()
    assert.deepEqual(state.emitted, ['close'])
    assert.equal(state.calls.length, 0)
    assert.ok(trigger.focuses > 0)
  })

  test(`${kind}: blank drafts cannot submit; successful default-member submission trims the identifier`, async () => {
    const { api, state } = setup(kind)
    api.draft.value = '  '
    await api.submit()
    assert.equal(state.calls.length, 0)
    api.draft.value = '  automation  '
    await api.submit()
    assert.deepEqual(state.calls, [{ value: 'automation', role: 'member' }])
    assert.deepEqual(state.emitted, ['close'])
    assert.equal(api.busy.value, false)
  })

  test(`${kind}: pending mutation blocks duplicate submits and cancellation`, async () => {
    const { api, state } = setup(kind)
    let resolve
    state.result = () => new Promise(done => { resolve = done })
    api.draft.value = 'automation'
    const pending = api.submit()
    assert.equal(api.busy.value, true)
    await api.submit()
    api.close() // Shared handler for Cancel and the native dialog's Escape event.
    assert.equal(state.calls.length, 1)
    assert.equal(state.nativeCloses, 0)
    assert.equal(state.emitted.length, 0)
    resolve(true)
    await pending
    assert.deepEqual(state.emitted, ['close'])
  })

  test(`${kind}: failed mutations retain the draft and role for a successful retry`, async () => {
    const { api, state, props } = setup(kind)
    api.draft.value = '  automation  '
    api.role.value = 'admin'
    state.result = async () => {
      props.errorMessage = 'The account is unavailable. Try again.'
      return false
    }
    await api.submit()
    assert.equal(api.error.value, props.errorMessage)
    assert.equal(api.draft.value, '  automation  ')
    assert.equal(api.role.value, 'admin')
    assert.equal(api.busy.value, false)
    assert.equal(state.emitted.length, 0)
    state.result = async () => true
    await api.submit()
    assert.equal(api.error.value, '')
    assert.deepEqual(state.calls[1], { value: 'automation', role: 'admin' })
    assert.deepEqual(state.emitted, ['close'])
  })

  test(`${kind}: thrown errors preserve the draft and leave cancellation available`, async () => {
    const { api, state } = setup(kind)
    api.draft.value = 'automation'
    state.result = async () => { throw new Error('Connection interrupted. Try again.') }
    await api.submit()
    assert.equal(api.error.value, 'Connection interrupted. Try again.')
    assert.equal(api.draft.value, 'automation')
    assert.equal(api.busy.value, false)
    api.close()
    assert.deepEqual(state.emitted, ['close'])
  })

  test(`${kind}: unmount suppresses late success and failure results`, async () => {
    for (const succeeds of [true, false]) {
      const { api, state, dispose } = setup(kind)
      let settle
      state.result = () => new Promise((resolve, reject) => {
        settle = () => succeeds ? resolve(true) : reject(new Error('Late failure'))
      })
      api.draft.value = 'automation'
      const pending = api.submit()
      dispose()
      settle()
      await pending
      assert.deepEqual(state.emitted, [])
      assert.equal(api.error.value, '')
    }
  })
}

function key(key) {
  return { key, prevented: false, stopped: false, preventDefault() { this.prevented = true }, stopPropagation() { this.stopped = true } }
}

test('member suggestions exclude existing members and select with keyboard before submitting', async () => {
  const { api, state, suggestions } = setup('member')
  suggestions.value = [{ user: 'existing', memberId: 'old@example.com' }, { user: 'new', memberId: 'new@example.com' }]
  api.draft.value = 'new'
  const down = key('ArrowDown')
  api.onUserKeydown(down)
  assert.equal(down.prevented, true)
  assert.equal(api.suggestions.value.length, 1)
  assert.equal(api.activeSuggestion.value, 0)
  const enter = key('Enter')
  api.onUserKeydown(enter)
  assert.equal(enter.prevented, true)
  assert.equal(api.draft.value, 'new@example.com')
  assert.equal(api.showSuggestions.value, false)
  assert.equal(state.calls.length, 0)
  await api.submit()
  assert.equal(state.calls[0].value, 'new@example.com')
})

test('Escape dismisses the open member suggestions before the modal', () => {
  const { api, state, suggestions } = setup('member')
  suggestions.value = [{ user: 'new', memberId: 'new@example.com' }]
  api.suggestionsOpen.value = true
  const firstEscape = key('Escape')
  api.onUserKeydown(firstEscape)
  assert.equal(firstEscape.prevented, true)
  assert.equal(firstEscape.stopped, true)
  assert.equal(api.showSuggestions.value, false)
  assert.equal(state.emitted.length, 0)
  const secondEscape = key('Escape')
  api.onUserKeydown(secondEscape)
  assert.equal(secondEscape.prevented, false)
  // The subsequent native cancel event invokes the dialog's close handler.
  api.close()
  assert.deepEqual(state.emitted, ['close'])
})
