// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('./kuery', async (importOriginal) => {
  const original = await importOriginal<typeof import('./kuery')>()
  const { computed } = await import('vue')
  return { ...original, useKueryApi: (context: any) => ({ api: computed(() => context.value && (context.value.fetch || context.value.token) ? {} : null), query: mocks.query }) }
})
vi.mock('./graph', async (importOriginal) => {
  const original = await importOriginal<typeof import('./graph')>()
  return { ...original, mountGraph: vi.fn(async () => ({ destroy() {}, fit() {}, restyle() {} })) }
})
import ImpactView from './components/ImpactView.vue'
import ResourcePage from './portalkit/ResourcePage.vue'

function deferred() {
  let resolve!: (value: any) => void
  let reject!: (error: Error) => void
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const snapshot = (cluster: string) => ({ objects: [{ id: cluster, cluster, relations: { members: [{ id: cluster, object: { kind: 'Node', metadata: { name: cluster } } }] } }] })
const anchor = { id: 'anchor', object: { kind: 'Deployment', metadata: { name: 'app' } } }
const button = (host: HTMLElement, label: string) => [...host.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === label)!

it.each([false, true])('restores focus after a ResourcePage retry with hidden header=%s', async hidden => {
  const state = reactive({ error: 'Offline' as string | null, loaded: false })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: 'resource', loaded: state.loaded, error: state.error, retryable: true, showHeader: !hidden, 'aria-label': hidden ? 'Custom name' : undefined, onRetry: () => { state.loaded = true; state.error = null } }) })
  try {
    app.mount(host); await flush()
    const retry = button(host, 'Retry'); retry.focus(); retry.click(); await flush()
    const root = host.querySelector<HTMLElement>('.k-resource-page')!
    expect(document.activeElement).toBe(hidden ? root : root.querySelector('.k-resource-page__title'))
    if (hidden) expect(root.getAttribute('aria-label')).toBe('Custom name')
  } finally { app.unmount(); host.remove() }
})

it('names a headerless page and never steals focus moved outside it', async () => {
  const state = reactive({ error: 'Offline' as string | null })
  const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: 'resource', loaded: true, error: state.error, retryable: true, showHeader: false }) })
  try {
    app.mount(host); await flush()
    expect(host.querySelector('.k-resource-page')?.getAttribute('aria-label')).toBe('Resource')
    button(host, 'Retry').focus(); state.error = null; outside.focus(); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})

it('does not restore focus after its page unmounts', async () => {
  const state = reactive({ error: 'Offline' as string | null, visible: true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => state.visible ? h(ResourcePage, { title: 'Resource', focusScope: 'resource', loaded: true, error: state.error, retryable: true }) : null })
  try {
    app.mount(host); await flush(); button(host, 'Retry').focus()
    state.error = null; state.visible = false; await flush()
    expect(document.activeElement).toBe(document.body)
  } finally { app.unmount(); host.remove() }
})

it('uses a custom header and does not move focus for a different page', async () => {
  const state = reactive({ first: 'Offline' as string | null, second: 'Offline' as string | null })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h('div', [
    h(ResourcePage, { title: 'First', focusScope: 'first', loaded: true, error: state.first, retryable: true }, { header: () => h('h2', { class: 'k-resource-page__title' }, 'Custom heading') }),
    h(ResourcePage, { title: 'Second', focusScope: 'second', loaded: true, error: state.second, retryable: true }),
  ]) })
  try {
    app.mount(host); await flush()
    const retries = host.querySelectorAll<HTMLButtonElement>('.k-resource-page__retry')
    retries[0].focus(); state.second = null; await flush()
    expect(document.activeElement).toBe(retries[0])
    state.first = null; await flush()
    expect(document.activeElement?.textContent).toBe('Custom heading')
  } finally { app.unmount(); host.remove() }
})

it('moves focus to the replacement retry when an initial error becomes stale', async () => {
  const state = reactive({ loaded: false })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: 'one', loaded: state.loaded, error: 'Offline', retryable: true }) })
  try {
    app.mount(host); await flush()
    const retry = button(host, 'Retry'); retry.focus(); state.loaded = true; await flush()
    const replacement = button(host, 'Retry')
    expect(replacement).not.toBe(retry)
    expect(document.activeElement).toBe(replacement)
    expect(replacement.getAttribute('tabindex')).not.toBe('-1')
  } finally { app.unmount(); host.remove() }
})

it('does not restore focus across a scope change or without opt-in', async () => {
  const state = reactive({ scope: 'one' as string | undefined, error: 'Offline' as string | null })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: state.scope, loaded: true, error: state.error, retryable: true }) })
  try {
    app.mount(host); await flush(); button(host, 'Retry').focus()
    state.scope = 'two'; state.error = null; await flush()
    expect(document.activeElement).toBe(document.body)
    state.scope = undefined; state.error = 'Offline'; await flush(); button(host, 'Retry').focus()
    state.error = null; await flush()
    expect(document.activeElement).toBe(document.body)
  } finally { app.unmount(); host.remove() }
})

it('does not restore an old-scope retry when the error clears in a later render', async () => {
  const state = reactive({ scope: 'one', error: 'Offline' as string | null })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: state.scope, loaded: true, error: state.error, retryable: true }) })
  try {
    app.mount(host); await flush(); button(host, 'Retry').focus()
    state.scope = 'two'; await flush()
    state.error = null; await flush()
    expect(document.activeElement).toBe(document.body)
    state.error = 'Offline'; await flush(); button(host, 'Retry').focus()
    state.error = null; await flush()
    expect(document.activeElement).toBe(host.querySelector('.k-resource-page__title'))
  } finally { app.unmount(); host.remove() }
})

it('accepts a fresh focus on the retained retry after a scope change', async () => {
  const state = reactive({ scope: 'one', error: 'Offline' as string | null })
  const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: state.scope, loaded: true, error: state.error, retryable: true }) })
  try {
    app.mount(host); await flush()
    const retry = button(host, 'Retry'); retry.focus()
    state.scope = 'two'; await flush()
    outside.focus(); retry.focus()
    state.error = null; await flush()
    expect(document.activeElement).toBe(host.querySelector('.k-resource-page__title'))
  } finally { app.unmount(); host.remove(); outside.remove() }
})

it('names the fallback when a custom header supplies no title element', async () => {
  const state = reactive({ error: 'Offline' as string | null })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourcePage, { title: 'Resource', focusScope: 'one', loaded: true, error: state.error, retryable: true }, { header: () => h('div', 'Custom') }) })
  try {
    app.mount(host); await flush(); button(host, 'Retry').focus(); state.error = null; await flush()
    const root = host.querySelector<HTMLElement>('.k-resource-page')!
    expect(document.activeElement).toBe(root)
    expect(root.getAttribute('aria-label')).toBe('Resource')
  } finally { app.unmount(); host.remove() }
})

it('clears the old scope during a delayed read and after failure, fencing late responses', async () => {
  const oldRefresh = deferred()
  const nextScope = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(oldRefresh.promise).mockReturnValueOnce(nextScope.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'old', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('old-edge')
    context.token = 'rotated'; await flush()
    context.tenant = 'new'; await flush()
    expect(host.textContent).not.toContain('old-edge')
    expect(host.textContent).not.toContain('old-edge')
    oldRefresh.resolve(snapshot('late-old-edge')); await flush()
    expect(host.textContent).not.toContain('late-old-edge')
    nextScope.reject(new Error('new scope failed')); await flush()
    expect(host.textContent).toContain('new scope failed')
    expect(host.textContent).not.toContain('old-edge')
  } finally { app.unmount(); host.remove() }
})

it('keeps the current snapshot on a same-scope refresh failure', async () => {
  const refresh = deferred()
  const retry = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('current-edge')).mockReturnValueOnce(refresh.promise).mockReturnValueOnce(retry.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    context.token = 'rotated'; await flush()
    expect(host.textContent).toContain('current-edge')
    expect(host.querySelector('.k-resource-page')?.getAttribute('aria-busy')).toBe('true')
    expect(host.querySelector('.k-resource-page__live')?.textContent).toContain('Updating app')
    refresh.reject(new Error('offline')); await flush()
    expect(host.textContent).toContain('offline')
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('status')
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('aria-live')).toBe('polite')
    button(host, 'Retry').click(); await flush()
    expect(host.querySelector('.k-resource-page')?.getAttribute('aria-busy')).toBe('true')
    expect(host.querySelector('.k-resource-page__live')?.textContent).toContain('Retrying app')
    retry.reject(new Error('still offline')); await flush()
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('alert')
    expect(host.textContent).toContain('current-edge')
  } finally { app.unmount(); host.remove() }
})

it('fences a delayed read when the host transport changes without changing scope', async () => {
  const oldRead = deferred()
  const newRead = deferred()
  mocks.query.mockReset().mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    context.fetch = vi.fn(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    oldRead.resolve(snapshot('old-transport')); await flush()
    expect(host.textContent).not.toContain('old-transport')
    newRead.resolve(snapshot('new-transport')); await flush()
    button(host, 'List').click(); await flush()
    expect(host.textContent).toContain('new-transport')
  } finally { app.unmount(); host.remove() }
})

it.each([false, true])('reports lost transport and recovers with loaded=%s', async (loaded) => {
  const pending = deferred()
  const recovery = deferred()
  mocks.query.mockReset()
  if (loaded) mocks.query.mockResolvedValueOnce(snapshot('current-edge'))
  mocks.query.mockReturnValueOnce(pending.promise).mockReturnValueOnce(recovery.promise)
  const context = reactive<{ basePath: string; tenant: string; fetch: any }>({ basePath: '/ui/providers/kuery', tenant: 'current', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    if (loaded) { button(host, 'List').click(); context.fetch = vi.fn(); await flush() }
    const signal = mocks.query.mock.calls.at(-1)![1] as AbortSignal
    context.fetch = null; await flush()
    expect(signal.aborted).toBe(true)
    expect(host.textContent).toContain('Kuery is waiting for workspace context.')
    expect(host.querySelector('.k-resource-page')?.getAttribute('aria-busy')).toBe('false')
    if (loaded) {
      expect(host.textContent).toContain('current-edge')
      expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('status')
    }
    pending.resolve(snapshot('late-edge')); await flush()
    expect(host.textContent).not.toContain('late-edge')
    context.fetch = vi.fn(); await flush()
    recovery.resolve(snapshot('recovered-edge')); await flush()
    if (!loaded) button(host, 'List').click()
    await flush()
    expect(host.textContent).toContain('recovered-edge')
    expect(host.textContent).not.toContain('waiting for workspace context')
  } finally { app.unmount(); host.remove() }
})

it('keeps a foreground retry assertive when transport is lost and reports unavailable retries', async () => {
  const retry = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('current-edge')).mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(retry.promise)
  const context = reactive<{ basePath: string; tenant: string; fetch: any }>({ basePath: '/ui/providers/kuery', tenant: 'current', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    context.fetch = vi.fn(); await flush()
    button(host, 'Retry').click(); await flush()
    context.fetch = null; await flush()
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('alert')
    expect(host.textContent).toContain('Kuery is waiting for workspace context.')
    const unavailableRetry = button(host, 'Retry')
    unavailableRetry.focus()
    unavailableRetry.click(); await flush()
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('alert')
    expect(host.textContent).toContain('Kuery is waiting for workspace context.')
    expect(button(host, 'Retry').disabled).toBe(false)
    expect(document.activeElement).toBe(unavailableRetry)
    expect(mocks.query).toHaveBeenCalledTimes(3)
    retry.resolve(snapshot('late-edge')); await flush()
    expect(host.textContent).not.toContain('late-edge')
  } finally { app.unmount(); host.remove() }
})

it('reports idle transport loss as a background stale status', async () => {
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('current-edge'))
  const context = reactive<{ basePath: string; tenant: string; fetch: any }>({ basePath: '/ui/providers/kuery', tenant: 'current', fetch: vi.fn() })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    context.fetch = null; await flush()
    expect(host.querySelector('.k-resource-page__stale')?.getAttribute('role')).toBe('status')
  } finally { app.unmount(); host.remove() }
})

it.each([false, true])('retains retry focus during a read and respects moved focus=%s', async (moveFocus) => {
  const retry = deferred()
  mocks.query.mockReset().mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(retry.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ImpactView, { context, anchor, savedView: 'view' }) })
  try {
    app.mount(host); await flush()
    const retryButton = button(host, 'Retry'); retryButton.focus(); retryButton.click(); await flush()
    expect(host.contains(retryButton)).toBe(true)
    if (moveFocus) button(host, 'List').focus()
    retry.resolve(snapshot('edge')); await flush()
    expect(document.activeElement).toBe(moveFocus ? button(host, 'List') : host.querySelector('.k-resource-page__title'))
  } finally { app.unmount(); host.remove() }
})


it('queues manual refresh behind a background read and preserves focus and snapshot on failure', async () => {
  const background = deferred(); const manual = deferred()
  mocks.query.mockReset().mockResolvedValueOnce(snapshot('old-edge')).mockReturnValueOnce(background.promise).mockReturnValueOnce(manual.promise)
  const context = reactive({ basePath: '/ui/providers/kuery', tenant: 'current', token: 'token' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(ImpactView, { context, anchor, savedView: 'view' })
  try {
    app.mount(host); await flush(); button(host, 'List').click(); await flush()
    context.token = 'rotated'; await flush()
    const refresh = button(host, 'Refresh'); refresh.focus(); refresh.click(); refresh.click(); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(2)
    expect(refresh.textContent).toContain('Refreshing')
    background.resolve(snapshot('updated-edge')); await flush()
    expect(mocks.query).toHaveBeenCalledTimes(3)
    manual.reject(new Error('offline')); await flush()
    expect(host.textContent).toContain('updated-edge')
    expect(host.textContent).toContain('offline')
    expect(document.activeElement).toBe(refresh)
    expect(button(host, 'List').getAttribute('aria-pressed')).toBe('true')
  } finally { app.unmount(); host.remove() }
})
