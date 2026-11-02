import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'

const { descriptor } = parse(readFileSync(new URL('./SelfHostWizard.vue', import.meta.url), 'utf8'))
const compiled = compileScript(descriptor, { id: 'self-host-wizard', inlineTemplate: true,
  templateOptions: { compilerOptions: { hoistStatic: false } } })
const code = ts.transpileModule(compiled.content, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
} }).outputText

function scenario({ resume = false, role = 'admin', multiple = false, manual = true, manualResume = false, installationPhase = 'Installing', registered = false, hidden = false } = {}) {
  const provider = { name: 'code', registered: false, ready: false }
  const calls = { register: 0, registerTarget: null, instructions: 0, load: 0, install: 0, status: 0 }
  const store = Vue.reactive({ byName: registered ? { code: { ...provider, registered: true } } : {}, error: null, installTargetsEligible: true, installTargetsLoaded: true,
    eligibleInstallTargets: [{ name: 'edge', workspace: 'ws' }, ...(multiple ? [{ name: 'other', workspace: 'ws' }] : [])],
    register: async (name, sourceProvider, target) => { calls.register++; calls.registerTarget = target; store.byName.code = { ...provider }; return { provider, kubeconfig: 'fixture-secret' } },
    instructions: async () => { calls.instructions++; return { provider, kubeconfig: 'fixture-secret' } },
    load: async () => { calls.load++; return true },
    installationState: { phase: installationPhase, message: installationPhase === 'NotStarted' ? 'Installation has not started.' : installationPhase === 'Failed' ? 'Helm installation failed.' : 'Installing Helm.', canRetry: installationPhase === 'Failed', attempt: 1 },
    startInstallation: async (name, target, retry) => { calls.install++; store.byName.code = { ...provider }; store.installationState = { phase: 'Installing', message: 'Installing Helm.', canRetry: false, attempt: retry ? 2 : 1 }; return { ...store.installationState } },
    getInstallation: async () => { calls.status++; return { ...store.installationState, phase: store.byName.code?.ready && store.installationState.phase !== 'Failed' ? 'Ready' : store.installationState.phase } },
  })
  let focused
  const events = []
  const timers = new Map()
  const visibilityListeners = new Set()
  const fakeDocument = {
    hidden,
    addEventListener(type, listener) { if (type === 'visibilitychange') visibilityListeners.add(listener) },
    removeEventListener(type, listener) { if (type === 'visibilitychange') visibilityListeners.delete(listener) },
    dispatchVisibility() { for (const listener of [...visibilityListeners]) listener() },
  }
  let timerID = 0
  const stub = { setup: (_, { slots }) => () => Vue.h('div', slots.default?.()) }
  const modules = {
    vue: Vue,
    '@/stores/orgProviders': { useOrgProvidersStore: () => store },
    '@/stores/tenant': { useTenantStore: () => Vue.reactive({ activeOrg: { role, displayName: 'Demo' } }) },
    '@/portalkit/CreateGuidance.vue': { default: { props: ['title', 'description', 'nextSteps'], setup: props => () => Vue.h('aside', { 'data-guidance': true }, [
      Vue.h('h3', props.title), Vue.h('p', props.description), ...(props.nextSteps ?? []).map(step => Vue.h('p', step)),
    ]) } },
    './SelfHostInstructions.vue': { default: { props: ['registration'], setup: props => () => Vue.h('div', { 'data-instructions': true }, props.registration.kubeconfig) } },
  }
  const exports = {}
  new Function('require', 'exports', 'setTimeout', 'clearTimeout', 'document', code)(
    id => modules[id] ?? new Proxy({ default: stub }, { get: (o, k) => o[k] ?? stub }), exports,
    (fn, delay) => { timers.set(++timerID, { fn, delay }); return timerID }, id => timers.delete(id), fakeDocument,
  )
  const renderer = Vue.createRenderer({
    createElement: type => Vue.markRaw({ type, props: {}, children: [], selectedIndex: -1,
      get options() { return this.children.filter(n => n.type === 'option') },
      get value() { return this.props.value }, focus() { focused = this }, addEventListener() {} }),
    createText: text => ({ type: '#text', text }), createComment: () => ({ type: '#comment' }),
    setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text },
    patchProp: (n, k, _, v) => { n.props[k] = v },
    insert(n, p, anchor) { n.parent = p; const i = p.children.indexOf(anchor); if (i < 0) p.children.push(n); else p.children.splice(i, 0, n) },
    remove(n) { const i = n.parent?.children.indexOf(n); if (i >= 0) n.parent.children.splice(i, 1) },
    parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1],
  })
  const root = { children: [] }
  const app = renderer.createApp(exports.default, { name: 'code', displayName: 'Code', resume, manual: manualResume,
    onCancel: () => events.push(['cancel']), onCompleted: name => events.push(['completed', name]), onManual: () => events.push(['manual']) })
  app.mount(root)
  return { root, store, calls, events, timers, manual, document: fakeDocument, visibilityListeners, focused: () => focused, cleanup: () => app.unmount(),
    setHidden(value) { fakeDocument.hidden = value; fakeDocument.dispatchVisibility() },
    async poll() { const [id, timeout] = timers.entries().next().value; timers.delete(id); await timeout.fn(); await settle() } }
}
const nodes = n => [n, ...(n.children ?? []).flatMap(nodes)]
const text = n => (n.text ?? '') + (n.children ?? []).map(text).join('')
const button = (s, label) => nodes(s.root).find(n => n.type === 'button' && text(n).includes(label))
const detailSummaries = s => nodes(s.root).filter(n => n.type === 'summary').map(text)
const settle = async () => { for (let i = 0; i < 8; i++) await Vue.nextTick() }
const submit = async s => { await nodes(s.root).find(n => n.type === 'form').props.onSubmit({ preventDefault() {} }); await settle() }
const create = async s => {
  if (s.manual) await button(s, 'Install manually').props.onClick()
  else await submit(s)
  await settle()
}

test('Configure creates once; instructions remain while status is pending, then readiness completes independently of enablement', async () => {
  const s = scenario()
  try {
    assert.equal(s.calls.register, 0)
    assert.match(text(s.root), /Review installation/)
    await create(s)
    assert.equal(s.calls.register, 1)
    assert.match(text(s.root), /fixture-secret/)
    assert.match(text(s.root), /Waiting for Code to register/)
    s.store.byName.code = { name: 'code', registered: true, ready: false }
    await s.poll()
    assert.match(text(s.root), /fixture-secret/)
    assert.match(text(s.root), /Waiting for Code to become ready/)
    assert.deepEqual(s.events, [['manual']])
    s.store.byName.code.ready = true
    await s.poll()
    assert.match(text(s.root), /Code is ready/)
    assert.doesNotMatch(text(s.root), /fixture-secret|Enable in/)
    assert.equal(s.timers.size, 0)
    button(s, 'Continue').props.onClick()
    assert.deepEqual(s.events, [['manual'], ['completed', 'code']])
  } finally { s.cleanup() }
})

test('Back to providers leaves pending registration and stops monitoring', async () => {
  const s = scenario()
  try {
    await create(s)
    button(s, 'Back to providers').props.onClick()
    await settle()
    assert.deepEqual(s.events, [['manual'], ['cancel']])
    assert.equal(s.store.byName.code.ready, false)
    assert.equal(s.timers.size, 0)
    assert.doesNotMatch(text(s.root), /fixture-secret/)
  } finally { s.cleanup() }
})

test('Resume reads the durable console operation without registering or fetching a credential', async () => {
  const s = scenario({ resume: true, registered: true })
  try {
    let resolveStatus
    s.store.getInstallation = () => { s.calls.status++; return new Promise(resolve => { resolveStatus = resolve }) }
    await settle()
    assert.equal(s.calls.status, 1)
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.register, 0)
    assert.doesNotMatch(text(s.root), /fixture-secret/)
    assert.match(text(s.root), /Checking installation status/)
    assert.doesNotMatch(text(s.root), /Installation not started/)
    resolveStatus(s.store.installationState)
    await settle()
    assert.match(text(s.root), /Installing Code/)
    assert.match(text(s.root), /Your provider is being set up/)
    assert.match(text(s.root), /Installation continues if you leave this page/)
    assert.doesNotMatch(text(s.root), /Installing Helm\.|Installation details|Readiness details and troubleshooting|While this runs/)
    assert.equal(s.timers.size, 1)
  } finally { s.cleanup() }
})

test('a stale manual resume stays on Review without creating a provider or fetching instructions', async () => {
  const s = scenario({ resume: true, manualResume: true })
  try {
    await settle()
    assert.match(text(s.root), /Review installation/)
    assert.equal(s.calls.register, 0)
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.install, 0)
    assert.equal(s.calls.status, 0)
    assert.equal(s.calls.load, 0)
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('hidden mounts defer status requests until the wizard becomes visible', async () => {
  const s = scenario({ resume: true, registered: true, manual: false, hidden: true })
  try {
    await settle()
    assert.equal(s.calls.load, 0)
    assert.equal(s.calls.status, 0)
    assert.equal(s.timers.size, 0)

    s.setHidden(false)
    await settle()
    assert.equal(s.calls.load, 1)
    assert.equal(s.calls.status, 1)
    assert.equal(s.timers.size, 1)
  } finally { s.cleanup() }
})

test('hidden manual resume never fetches credentials on mount or visibility restoration', async () => {
  const s = scenario({ resume: true, registered: true, manual: false, manualResume: true, hidden: true })
  try {
    await settle()
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.load, 0)
    assert.equal(s.calls.status, 0)

    s.setHidden(false)
    await settle()
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.load, 1)
    assert.equal(s.calls.status, 0)
    assert.match(text(s.root), /Show manual commands/)

    await button(s, 'Show manual commands').props.onClick()
    await settle()
    assert.equal(s.calls.instructions, 1, 'a visible explicit action retrieves manual commands')
    assert.match(text(s.root), /fixture-secret/)
  } finally { s.cleanup() }
})

test('pending readiness polling pauses while hidden and resumes immediately without fetching credentials', async () => {
  const s = scenario()
  try {
    await create(s)
    const loadsBeforeHide = s.calls.load
    const instructionsBeforeHide = s.calls.instructions
    assert.equal(s.timers.size, 1)
    assert.equal(s.timers.values().next().value.delay, 2500)

    s.setHidden(true)
    assert.equal(s.timers.size, 0)
    assert.equal(s.calls.load, loadsBeforeHide)

    s.setHidden(false)
    await settle()
    assert.equal(s.calls.load, loadsBeforeHide + 1)
    assert.equal(s.calls.instructions, instructionsBeforeHide)
    assert.equal(s.timers.size, 1)
  } finally { s.cleanup() }
})

test('sustained readiness polling backs off after five fast checks', async () => {
  const s = scenario()
  try {
    await create(s)
    assert.equal(s.timers.values().next().value.delay, 2500)
    for (let i = 0; i < 4; i++) await s.poll()
    assert.equal(s.timers.values().next().value.delay, 2500)
    await s.poll()
    assert.equal(s.timers.values().next().value.delay, 15000)
  } finally { s.cleanup() }
})

test('visibility restoration during an in-flight check runs one immediate follow-up without overlap', async () => {
  const s = scenario({ resume: true, registered: true, manual: false })
  try {
    await settle()
    const loadCount = s.calls.load
    let resolvePendingLoad
    let first = true
    s.store.load = () => {
      s.calls.load++
      if (first) {
        first = false
        return new Promise(resolve => { resolvePendingLoad = resolve })
      }
      return Promise.resolve(true)
    }

    const pendingCheck = s.poll()
    await settle()
    assert.equal(s.calls.load, loadCount + 1)
    s.setHidden(true)
    s.setHidden(false)
    await settle()
    assert.equal(s.calls.load, loadCount + 1, 'visible restoration waits for the active request instead of overlapping it')

    resolvePendingLoad(true)
    await pendingCheck
    await settle()
    assert.equal(s.calls.load, loadCount + 2, 'one immediate check starts after the active request settles')
    assert.equal(s.timers.size, 1)
  } finally { s.cleanup() }
})

test('a refresh that settles while hidden does not start another request or rearm polling', async () => {
  const s = scenario({ resume: true, registered: true, manual: false })
  let resolveLoad
  try {
    await settle()
    const statuses = s.calls.status
    s.store.load = () => new Promise(resolve => { resolveLoad = resolve })
    const pendingCheck = s.poll()
    await settle()
    s.setHidden(true)
    resolveLoad(true)
    await pendingCheck

    assert.equal(s.calls.status, statuses, 'the installation status request is skipped after the page becomes hidden')
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('Console progress stays concise during normal phases and exposes diagnostics on failure', async () => {
  const cases = [
    { phase: 'Preparing', headline: 'Getting things ready', status: 'Your provider is being set up' },
    { phase: 'Queued', headline: 'Installation queued', status: 'Your provider is being set up' },
    { phase: 'Installing', headline: 'Installing Code', status: 'Your provider is being set up' },
    { phase: 'Checking', headline: 'Checking that Code is ready', status: 'Making sure Code is ready' },
    { phase: 'Ready', headline: 'Waiting for Code to be ready', status: 'Waiting for Code to report ready' },
  ]
  for (const { phase, headline, status } of cases) {
    const s = scenario({ resume: true, registered: true, manual: false, installationPhase: phase })
    try {
      await settle()
      assert.match(text(s.root), new RegExp(headline), phase)
      assert.match(text(s.root), new RegExp(status), phase)
      assert.match(text(s.root), /Installation continues if you leave this page/)
      assert.deepEqual(detailSummaries(s), [], `${phase} hides diagnostic disclosures`)
      assert.doesNotMatch(text(s.root), /Installing Helm\.|Helm installation failed|Installation tasks|While this runs/)
    } finally { s.cleanup() }
  }

  const failed = scenario({ resume: true, registered: true, manual: false, installationPhase: 'Failed' })
  try {
    await settle()
    assert.match(text(failed.root), /Installation needs attention/)
    assert.match(text(failed.root), /Helm installation failed/)
    assert.deepEqual(detailSummaries(failed), ['Installation details', 'Readiness details and troubleshooting'])
    assert.match(text(failed.root), /Next steps/)
    assert.match(text(failed.root), /Review the status message and installation details/)
    assert.match(text(failed.root), /Retry the installation when ready/)
    assert.doesNotMatch(text(failed.root), /Installation continues if you leave this page/)
    assert.equal(failed.timers.size, 0, 'terminal failure does not claim automatic polling')
  } finally { failed.cleanup() }
})

test('Install manually advances directly to generated commands while console install stays primary', async () => {
  const s = scenario({ manual: false })
  try {
    assert.equal(s.calls.register, 0)
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.install, 0)
    assert.doesNotMatch(text(s.root), /Advanced options/)
    const manual = button(s, 'Install manually')
    const automatic = button(s, 'Install on edge')
    assert.match(manual.props.class, /k-btn--ghost/)
    assert.match(automatic.props.class, /k-btn--primary/)
    await manual.props.onClick()
    await settle()
    assert.match(text(s.root), /Install the provider in your cluster/)
    assert.match(text(s.root), /fixture-secret/)
    assert.equal(s.calls.register, 1)
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.install, 0)
    assert.equal(s.calls.status, 0)
    assert.deepEqual(s.events, [['manual']])
  } finally { s.cleanup() }
})

test('read-only members can resume status but cannot retry a failed installation', async () => {
  const s = scenario({ resume: true, registered: true, role: 'member' })
  try {
    await settle()
    assert.equal(s.calls.status, 1)
    s.store.installationState = { phase: 'Failed', message: 'Helm installation failed.', canRetry: true, attempt: 1 }
    await s.poll()
    assert.match(text(s.root), /Helm installation failed/)
    assert.equal(button(s, 'Retry installation').props.disabled, true)
    await button(s, 'Retry installation').props.onClick()
    await settle()
    assert.equal(s.calls.install, 0)
    assert.equal(s.calls.instructions, 0)
    assert.equal(s.calls.register, 0)
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('A failed readiness refresh preserves instructions and cannot complete from stale ready data', async () => {
  const s = scenario()
  try {
    await create(s)
    assert.deepEqual(detailSummaries(s), [])
    s.store.byName.code.ready = true
    s.store.error = 'network unavailable'
    await s.poll()
    assert.match(text(s.root), /Could not refresh status/)
    assert.match(text(s.root), /fixture-secret/)
    assert.deepEqual(detailSummaries(s), ['Readiness details and troubleshooting'])
    assert.equal(s.timers.size, 1)
    s.store.error = null
    await s.poll()
    assert.match(text(s.root), /Code is ready/)
  } finally { s.cleanup() }
})

test('A failed console status refresh offers Check again instead of promising an automatic retry', async () => {
  const s = scenario({ resume: true, registered: true, manual: false, installationPhase: 'Failed' })
  try {
    await settle()
    assert.equal(s.timers.size, 0)
    s.store.getInstallation = async () => { throw new Error('offline') }
    await button(s, 'Check again').props.onClick()
    await settle()
    assert.match(text(s.root), /Could not refresh status/)
    assert.match(text(s.root), /Check again/)
    assert.doesNotMatch(text(s.root), /We’ll retry automatically/)
    assert.match(text(s.root), /Installation needs attention/)
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('terminal installation failure stays stopped on visibility changes until Check again is explicit', async () => {
  const s = scenario({ resume: true, registered: true, manual: false, installationPhase: 'Failed' })
  try {
    await settle()
    const loads = s.calls.load
    const statuses = s.calls.status
    assert.equal(s.timers.size, 0)

    s.setHidden(true)
    s.setHidden(false)
    await settle()
    assert.equal(s.calls.load, loads)
    assert.equal(s.calls.status, statuses)
    assert.equal(s.timers.size, 0)

    await button(s, 'Check again').props.onClick()
    await settle()
    assert.equal(s.calls.load, loads + 1)
    assert.equal(s.calls.status, statuses + 1)
    assert.equal(s.timers.size, 0, 'a repeated terminal failure remains stopped')
  } finally { s.cleanup() }
})

test('Unmount during registration discards late credentials and never starts monitoring', async () => {
  const s = scenario()
  let resolve
  s.store.register = () => new Promise(done => { resolve = done })
  const pending = create(s)
  await settle()
  assert.equal(button(s, 'Install manually').props.disabled, true)
  assert.equal(nodes(s.root).find(n => n.type === 'button' && n.props.type === 'submit').props.disabled, true)
  s.cleanup()
  resolve({ provider: { name: 'code', ready: true }, kubeconfig: 'late-secret' })
  await pending
  assert.equal(s.timers.size, 0)
  assert.equal(s.events.length, 0)
})

test('unmount during console start ignores the late status and never starts monitoring', async () => {
  const s = scenario({ manual: false })
  let resolve
  s.store.startInstallation = () => new Promise(done => { resolve = done })
  const pending = create(s)
  s.cleanup()
  resolve({ phase: 'Installing', message: 'Installing Helm.', canRetry: false })
  await pending
  assert.equal(s.timers.size, 0)
  assert.equal(s.events.length, 0)
})

test('unmount during readiness refresh removes the listener and never rearms polling', async () => {
  const s = scenario({ resume: true, registered: true, manual: false })
  let resolveLoad
  try {
    await settle()
    assert.equal(s.timers.size, 1)
    s.store.load = () => new Promise(resolve => { resolveLoad = resolve })
    const pending = s.poll()
    await settle()
    assert.equal(s.visibilityListeners.size, 1)

    s.cleanup()
    assert.equal(s.visibilityListeners.size, 0)
    resolveLoad(true)
    await pending
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('Organization members cannot register under admin-only policy', async () => {
  const s = scenario({ role: 'member' })
  try {
    await settle()
    assert.equal(button(s, 'Install manually').props.disabled, true)
    assert.equal(button(s, 'Install on edge').props.disabled, true)
    await create(s)
    assert.equal(s.calls.register, 0)
  } finally { s.cleanup() }
})

test('Organization members cannot start console installation under admin-only policy', async () => {
  const s = scenario({ role: 'member', manual: false })
  try {
    await settle()
    assert.equal(button(s, 'Install on edge').props.disabled, true)
    await create(s)
    assert.equal(s.calls.install, 0)
    assert.equal(s.calls.register, 0)
    assert.equal(s.timers.size, 0)
  } finally { s.cleanup() }
})

test('multiple clusters require an explicit choice before registration', async () => {
  const s = scenario({ multiple: true })
  try {
    await settle()
    assert.equal(button(s, 'Install manually').props.disabled, true)
    await create(s)
    assert.equal(s.calls.register, 0)
    nodes(s.root).find(n => n.type === 'select').props['onUpdate:modelValue']('ws/other')
    await settle()
    assert.equal(button(s, 'Install manually').props.disabled, false)

    s.store.eligibleInstallTargets = [
      { name: 'edge-2', workspace: 'ws-2' },
      { name: 'edge-3', workspace: 'ws-3' },
    ]
    await settle()
    assert.equal(button(s, 'Install manually').props.disabled, true, 'a removed selection cannot fall back to another Edge')
    await create(s)
    assert.equal(s.calls.register, 0)

    nodes(s.root).find(n => n.type === 'select').props['onUpdate:modelValue']('ws-3/edge-3')
    await settle()
    assert.equal(button(s, 'Install manually').props.disabled, false)
    await create(s)
    assert.equal(s.calls.register, 1)
    assert.deepEqual(s.calls.registerTarget, { workspace: 'ws-3', name: 'edge-3' })
  } finally { s.cleanup() }
})

test('Review distinguishes loading connected clusters from a confirmed lack of eligible targets', async () => {
  const s = scenario()
  try {
    s.store.installTargetsLoaded = false
    s.store.installTargetsEligible = false
    await settle()
    assert.match(text(s.root), /Checking connected clusters/)
    assert.doesNotMatch(text(s.root), /No connected Kubernetes cluster is available/)
    assert.equal(button(s, 'Install manually').props.disabled, true)

    s.store.installTargetsLoaded = true
    await settle()
    assert.doesNotMatch(text(s.root), /Checking connected clusters/)
    assert.match(text(s.root), /No connected Kubernetes cluster is available/)
    assert.equal(button(s, 'Install manually').props.disabled, true)
  } finally { s.cleanup() }
})


test('opening the wizard focuses its title, and later stages focus the current heading', async () => {
  const s = scenario()
  try {
    await settle()
    assert.equal(text(s.focused()), 'Install Code')
    await create(s)
    assert.equal(text(s.focused()), 'Install the provider in your cluster')
    s.store.byName.code.ready = true
    await s.poll()
    assert.equal(text(s.focused()), 'Code is ready')
  } finally { s.cleanup() }
})

test('a rejected manual registration keeps console install primary and allows automatic fallback', async () => {
  const s = scenario()
  try {
    s.store.register = async () => { s.calls.register++; throw new Error('Provider setup is blocked by the organization’s workspace limit.') }
    await create(s)
    assert.equal(s.calls.register, 1)
    assert.match(text(s.root), /organization’s workspace limit/)
    assert.match(text(s.root), /Railgrid will install the provider through this cluster/)
    assert.match(text(s.root), /Review installation/)
    assert.equal(button(s, 'Install manually').props.disabled, false)
    assert.equal(button(s, 'Install on edge').props.disabled, false)
    assert.match(button(s, 'Install on edge').props.class, /k-btn--primary/)
    assert.equal(s.timers.size, 0)
    assert.equal(s.events.length, 0)
    assert.doesNotMatch(text(s.root), /fixture-secret/)

    await submit(s)
    assert.equal(s.calls.install, 1)
    assert.equal(s.calls.register, 1)
    assert.match(text(s.root), /Installing Code/)
  } finally { s.cleanup() }
})


test('console installation shows durable progress and retries a failed Job without exposing a credential', async () => {
 const s = scenario({ manual: false })
 try {
  await create(s)
  assert.equal(s.calls.install, 1)
  assert.equal(s.calls.register, 0)
  assert.equal(s.calls.instructions, 0)
  assert.doesNotMatch(text(s.root), /fixture-secret/)
  assert.match(text(s.root), /Installing Code/)
  assert.match(text(s.root), /Your provider is being set up/)
  assert.doesNotMatch(text(s.root), /Installing Helm\.|Installation details|Readiness details and troubleshooting/)
  s.store.installationState = { phase: 'Failed', message: 'Helm installation failed.', canRetry: true, attempt: 1 }
  await s.poll()
  assert.match(text(s.root), /Helm installation failed/)
  assert.doesNotMatch(text(s.root), /Checking automatically/)
  assert.equal(s.timers.size, 0)
  await button(s, 'Retry installation').props.onClick()
  await settle()
  assert.equal(s.calls.install, 2)
  assert.equal(s.store.installationState.attempt, 2)
  s.store.byName.code.ready = true
  await s.poll()
  assert.match(text(s.root), /Code is ready/)
  assert.equal(s.calls.instructions, 0)
  button(s, 'Continue').props.onClick()
  assert.deepEqual(s.events, [['completed', 'code']])
 } finally { s.cleanup() }
})

test('failed console installation can continue manually without offering a console switch', async () => {
 const s = scenario({ manual: false })
 try {
  await create(s)
  assert.equal(s.calls.install, 1)
  s.store.installationState = { phase: 'Failed', message: 'Helm installation failed.', canRetry: false, attempt: 1 }
  await s.poll()
  assert.match(text(s.root), /Helm installation failed/)
  assert.match(text(s.root), /Install manually/)
  s.store.eligibleInstallTargets = []
  s.store.installTargetsEligible = false

  await button(s, 'Install manually').props.onClick()
  await settle()
  assert.equal(s.calls.instructions, 1)
  assert.match(text(s.root), /fixture-secret/)
  assert.equal(s.timers.size, 1)

  assert.equal(button(s, 'Use console installation'), undefined)
  assert.equal(s.calls.install, 1)
  assert.equal(s.calls.instructions, 1)
  assert.equal(s.calls.status, 2)
  assert.equal(s.timers.size, 1)
  s.store.byName.code.ready = true
  await s.poll()
  assert.match(text(s.root), /Code is ready/)
  assert.doesNotMatch(text(s.root), /fixture-secret/)
 } finally { s.cleanup() }
})

test('a failed manual instruction request retries instructions instead of retrying the console Job', async () => {
 const s = scenario({ manual: false })
 try {
  const instructions = s.store.instructions
  await create(s)
  s.store.installationState = { phase: 'Failed', message: 'Helm installation failed.', canRetry: true, attempt: 1 }
  await s.poll()
  s.store.instructions = async () => { s.calls.instructions++; throw new Error('credential service unavailable') }

  await button(s, 'Install manually').props.onClick()
  await settle()
  assert.equal(s.calls.install, 1)
  assert.equal(s.calls.instructions, 1)
  assert.match(text(s.root), /credential service unavailable/)
  assert.equal(button(s, 'Retry manual instructions').props.disabled, false)

  s.store.instructions = instructions
  await button(s, 'Retry manual instructions').props.onClick()
  await settle()
  assert.equal(s.calls.install, 1)
  assert.equal(s.calls.instructions, 2)
  assert.match(text(s.root), /fixture-secret/)
  assert.equal(button(s, 'Use console installation'), undefined)
 } finally { s.cleanup() }
})

test('a resumed manual wizard returns to Review when its provider disappears before credential retry', async () => {
 const s = scenario({ resume: true, registered: true, manual: false, manualResume: true, multiple: true })
 try {
  s.store.instructions = async () => { s.calls.instructions++; throw new Error('credential service unavailable') }
  await settle()
  assert.equal(s.calls.instructions, 1)
  assert.match(text(s.root), /credential service unavailable/)
  assert.equal(button(s, 'Retry manual instructions').props.disabled, false)

  delete s.store.byName.code
  await button(s, 'Retry manual instructions').props.onClick()
  await settle()

  assert.match(text(s.root), /Choose a cluster/)
  assert.equal(s.calls.instructions, 1)
  assert.equal(s.calls.register, 0)
  assert.equal(s.calls.install, 0)
  assert.equal(s.calls.status, 0)
  assert.equal(s.timers.size, 0)
  assert.doesNotMatch(text(s.root), /fixture-secret/)
 } finally { s.cleanup() }
})

test('a readiness refresh that confirms provider removal clears manual instructions and stops polling', async () => {
 const s = scenario({ resume: true, registered: true, manual: false, manualResume: true })
 try {
  await settle()
  assert.equal(s.calls.instructions, 1)
  assert.equal(s.timers.size, 1)
  assert.match(text(s.root), /fixture-secret/)

  delete s.store.byName.code
  await s.poll()

  assert.match(text(s.root), /Review installation/)
  assert.equal(s.calls.instructions, 1)
  assert.equal(s.calls.register, 0)
  assert.equal(s.timers.size, 0)
  assert.doesNotMatch(text(s.root), /fixture-secret/)
 } finally { s.cleanup() }
})

test('a resumed operation with no console Job monitors readiness and requires an explicit manual credential fetch', async () => {
 const s = scenario({ resume: true, registered: true, manual: false, installationPhase: 'NotStarted' })
 try {
  await settle()
  assert.equal(s.calls.status, 1)
  assert.equal(s.calls.install, 0)
  assert.equal(s.calls.instructions, 0)
  assert.doesNotMatch(text(s.root), /fixture-secret/)
  assert.equal(button(s, 'Install provider'), undefined, 'do not start a console install on an existing registration with no console plan')
  assert.equal(button(s, 'Show manual commands').props.disabled, false)

  await button(s, 'Show manual commands').props.onClick()
  await settle()
  assert.equal(s.calls.instructions, 1)
  assert.equal(s.calls.install, 0)
  assert.match(text(s.root), /fixture-secret/)
 } finally { s.cleanup() }
})

test('an explicit manual route restores instructions and readiness; a viewer receives no credential', async () => {
 const admin = scenario({ resume: true, registered: true, manual: false, manualResume: true })
 try {
  await settle()
  assert.equal(admin.calls.instructions, 1)
  assert.equal(admin.calls.status, 0)
  assert.equal(admin.calls.install, 0)
  assert.match(text(admin.root), /fixture-secret/)
  assert.equal(admin.events.filter(event => event[0] === 'manual').length, 1)
  assert.equal(admin.timers.size, 1)
 } finally { admin.cleanup() }

 const member = scenario({ resume: true, registered: true, role: 'member', manual: false, manualResume: true })
 try {
  await settle()
  assert.equal(member.calls.instructions, 0)
  assert.equal(member.calls.status, 0)
  assert.equal(member.calls.install, 0)
  assert.doesNotMatch(text(member.root), /fixture-secret/)
  assert.match(text(member.root), /Ask an organization admin/)
  assert.equal(member.timers.size, 1)
 } finally { member.cleanup() }
})

test('leaving a console install stops polling without cancelling its durable operation', async () => {
 const s = scenario({ manual: false })
 try {
  await create(s)
  button(s, 'Back to providers').props.onClick()
  assert.equal(s.calls.install, 1)
  assert.equal(s.timers.size, 0)
  assert.deepEqual(s.events, [['cancel']])
 } finally { s.cleanup() }
})

test('a console status failure preserves progress and cannot complete from stale readiness', async () => {
 const s = scenario({ manual: false })
 try {
  await create(s)
  s.store.byName.code.ready = true
  s.store.getInstallation = async () => { throw new Error('offline') }
  await s.poll()
  assert.match(text(s.root), /Could not refresh status/)
  assert.match(text(s.root), /Installing Helm\./)
  assert.deepEqual(detailSummaries(s), ['Installation details', 'Readiness details and troubleshooting'])
  assert.doesNotMatch(text(s.root), /Code is ready/)
  assert.equal(s.timers.size, 1)
 } finally { s.cleanup() }
})
