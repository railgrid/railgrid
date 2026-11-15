// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import CodeExplorer from '../src/CodeExplorer.vue'
import { api } from '../src/api'
import { resolveConfirm } from '../src/portalkit/confirm'
const flush = async () => { for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() } }
const deferred = <T,>() => { let resolve!: (value: T) => void; let reject!: (reason: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
afterEach(() => vi.restoreAllMocks())
it('opens another file while a previous read stalls and ignores its late result', async () => {
  const pending = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValue({ files: [{ path: 'a.txt', size: 1 }, { path: 'b.txt', size: 1 }] } as never)
  const read = vi.spyOn(api, 'readProjectFile').mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ path: 'b.txt', content: 'new file', size: 8, binary: false } as never)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, projectName: 'one', refreshRevision: 0 }) })
  try {
    app.mount(host); await flush()
    const firstSignal = read.mock.calls[0][3]!
    ;[...host.querySelectorAll<HTMLButtonElement>('[role="treeitem"]')].find(item => item.textContent?.includes('b.txt'))!.click()
    await flush()
    expect(firstSignal.aborted).toBe(true)
    expect(host.textContent).toContain('new file')
    pending.resolve({ path: 'a.txt', content: 'old file', size: 8, binary: false } as never); await flush()
    expect(host.textContent).toContain('new file'); expect(host.textContent).not.toContain('old file')
  } finally { app.unmount(); host.remove() }
})
it('does not start a file read when a refresh completes after unmount', async () => {
  const pending = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [{ path: 'a.txt', size: 1 }] } as never).mockReturnValueOnce(pending.promise)
  const read = vi.spyOn(api, 'readProjectFile').mockResolvedValue({ path: 'a.txt', content: 'file', size: 4, binary: false } as never)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  app.mount(host); await flush(); state.refreshRevision++; await flush()
  app.unmount(); pending.resolve({ files: [{ path: 'a.txt', size: 1 }] } as never); await flush()
  expect(read).toHaveBeenCalledTimes(1); host.remove()
})
it('does not reload after a file creation finishes after unmount', async () => {
  const pending = deferred<never>()
  const list = vi.spyOn(api, 'listProjectFiles').mockResolvedValue({ files: [] } as never)
  vi.spyOn(api, 'putProjectFile').mockReturnValue(pending.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, projectName: 'one', refreshRevision: 0 }) })
  app.mount(host); await flush()
  host.querySelector<HTMLButtonElement>('button[aria-label="New file"]')!.click(); await flush()
  const input = host.querySelector<HTMLInputElement>('input[placeholder="src/notes.md"]')!
  input.value = 'new.txt'; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
  host.querySelector<HTMLFormElement>('form[aria-label="New file"]')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush()
  app.unmount(); pending.resolve({} as never); await flush()
  expect(list).toHaveBeenCalledTimes(1); host.remove()
})
it('does not let an earlier project session finish into a return to the same project', async () => {
  const oldWrite = deferred<never>(); const newWrite = deferred<never>()
  const list = vi.spyOn(api, 'listProjectFiles').mockResolvedValue({ files: [] } as never)
  const put = vi.spyOn(api, 'putProjectFile').mockReturnValueOnce(oldWrite.promise).mockReturnValueOnce(newWrite.promise)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  const create = async (name: string) => {
    host.querySelector<HTMLButtonElement>('button[aria-label="New file"]')!.click(); await flush()
    const input = host.querySelector<HTMLInputElement>('input[placeholder="src/notes.md"]')!
    input.value = name; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
    host.querySelector<HTMLFormElement>('form[aria-label="New file"]')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush()
  }
  try {
    app.mount(host); await flush(); await create('old.txt')
    state.projectName = 'two'; await flush(); state.projectName = 'one'; await flush()
    await create('new.txt'); expect(put).toHaveBeenCalledTimes(2)
    oldWrite.resolve({} as never); await flush()
    expect(list).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('Creating new.txt')
    expect(host.querySelector<HTMLButtonElement>('form[aria-label="New file"] button[type="submit"]')!.disabled).toBe(true)
    newWrite.resolve({} as never); await flush()
  } finally { app.unmount(); host.remove() }
})
it('does not delete after confirmation from an earlier project session', async () => {
  vi.spyOn(api, 'listProjectFiles').mockResolvedValue({ files: [{ path: 'a.txt', size: 1 }] } as never)
  vi.spyOn(api, 'readProjectFile').mockResolvedValue({ path: 'a.txt', content: 'file', size: 4, binary: false, version: 'sha256:same' } as never)
  const remove = vi.spyOn(api, 'deleteProjectFile').mockResolvedValue({} as never)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('button[aria-label="Delete a.txt"]')!.click(); await flush()
    state.projectName = 'two'; await flush(); state.projectName = 'one'; await flush()
    resolveConfirm(true); await flush()
    expect(remove).not.toHaveBeenCalled()
  } finally { resolveConfirm(false); app.unmount(); host.remove() }
})
it('retains an empty snapshot and serializes queued reads', async () => {
  const pending = deferred<never>()
  const list = vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [] } as never).mockReturnValueOnce(pending.promise).mockResolvedValue({ files: [] } as never)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); expect(host.textContent).toContain('No files yet')
    state.refreshRevision++; await flush(); state.refreshRevision++; await flush()
    expect(list).toHaveBeenCalledTimes(2); expect(host.textContent).toContain('No files yet')
    pending.reject(new Error('Offline')); await flush()
    expect(list).toHaveBeenCalledTimes(3); expect(host.textContent).toContain('No files yet')
  } finally { app.unmount(); host.remove() }
})
it('retains the selected file on failure and clears it on project change', async () => {
  const pending = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockImplementation(async (_ctx, name) => ({ files: name === 'one' ? [{ path: 'a.txt', size: 3 }] : [] }) as never)
  const read = vi.spyOn(api, 'readProjectFile').mockResolvedValueOnce({ path: 'a.txt', content: 'original', size: 8, binary: false } as never).mockReturnValueOnce(pending.promise)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); expect(host.textContent).toContain('original')
    state.refreshRevision++; await flush(); expect(read).toHaveBeenCalledTimes(2); expect(host.textContent).toContain('original')
    pending.reject(new Error('Offline')); await flush()
    expect(host.textContent).toContain('Showing the last loaded file'); expect(host.textContent).toContain('original')
    state.projectName = 'two'; await flush(); expect(host.textContent).not.toContain('original'); expect(host.textContent).toContain('No files yet')
  } finally { app.unmount(); host.remove() }
})
it('keeps an empty tree visible after a failed read', async () => {
  const retry = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [] } as never).mockRejectedValueOnce(new Error('Offline')).mockReturnValueOnce(retry.promise)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); state.refreshRevision++; await flush()
    expect(host.textContent).toContain('Offline')
    expect(host.textContent).toContain('Showing the last loaded tree')
    expect(host.textContent).toContain('No files yet')
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Retry refresh')!
    button.focus(); button.click(); await flush()
    expect(document.activeElement).toBe(button)
    retry.resolve({ files: [] } as never); await flush()
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Refresh workspace files')
  } finally { app.unmount(); host.remove() }
})
it('does not publish a late tree from a previous project', async () => {
  const pending = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockReturnValueOnce(pending.promise).mockResolvedValue({ files: [] } as never)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); state.projectName = 'two'; await flush()
    pending.resolve({ files: [{ path: 'old-secret.txt' }] } as never); await flush()
    expect(host.textContent).not.toContain('old-secret.txt')
    expect(host.textContent).toContain('No files yet')
  } finally { app.unmount(); host.remove() }
})
it('does not steal focus if it moved while retry was pending', async () => {
  const retry = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [] } as never).mockRejectedValueOnce(new Error('Offline')).mockReturnValueOnce(retry.promise)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const outside = document.createElement('button'); document.body.append(outside)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); state.refreshRevision++; await flush()
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Retry refresh')!
    button.focus(); button.click(); await flush(); outside.focus()
    retry.resolve({ files: [] } as never); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})
it('returns retry focus to the available refresh control while a queued read runs', async () => {
  const retry = deferred<never>(); const queued = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [] } as never).mockRejectedValueOnce(new Error('Offline')).mockReturnValueOnce(retry.promise).mockReturnValueOnce(queued.promise)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); state.refreshRevision++; await flush()
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Retry refresh')!
    button.focus(); button.click(); await flush(); state.refreshRevision++; await flush()
    retry.resolve({ files: [] } as never); await flush()
    const refresh = host.querySelector<HTMLButtonElement>('[aria-label="Refresh workspace files"]')!
    expect(document.activeElement).toBe(refresh)
    expect(refresh.getAttribute('aria-disabled')).toBe('true')
    queued.resolve({ files: [] } as never); await flush()
  } finally { app.unmount(); host.remove() }
})
it('does not restore retry focus after switching projects', async () => {
  const retry = deferred<never>()
  vi.spyOn(api, 'listProjectFiles').mockResolvedValueOnce({ files: [] } as never).mockRejectedValueOnce(new Error('Offline')).mockReturnValueOnce(retry.promise).mockResolvedValue({ files: [] } as never)
  const state = reactive({ projectName: 'one', refreshRevision: 0 })
  const host = document.createElement('div'); document.body.append(host)
  const outside = document.createElement('button'); document.body.append(outside)
  const app = createApp({ render: () => h(CodeExplorer, { ctx: null, ...state }) })
  try {
    app.mount(host); await flush(); state.refreshRevision++; await flush()
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Retry refresh')!
    button.focus(); button.click(); await flush()
    state.projectName = 'two'; await flush(); outside.focus()
    retry.resolve({ files: [] } as never); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})
