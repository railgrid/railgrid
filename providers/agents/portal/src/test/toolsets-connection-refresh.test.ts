import { expect, it, vi } from 'vitest'
import Toolsets from '../views/Toolsets.vue'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

it.each([
  [false, 'Tool connections could not be loaded.'],
  [true, 'Tool connections could not be refreshed. The last loaded connections may be out of date.'],
])('describes the connection error accurately with snapshot=%s', async (hasSnapshot, message) => {
  const api = stubApi()
  const store = makeStore(api)
  Object.assign(store.connections, { loaded: true, hasSnapshot, error: 'Offline' })
  Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, data: [{ metadata: { name: 'item' }, spec: { connections: [] } }] })
  const view = await mountVue(Toolsets, { store, api })
  expect(view.element.textContent).toContain(message)
  view.unmount()
})

it.each([
  ['create', { createRoute: true }, false],
  ['collection first run', {}, false],
  ['detail', { editRoute: true, editName: 'item' }, true],
])('queues a foreground connection retry during a background read on %s', async (_name, props, snapshot) => {
  const completions: Array<{ resolve: (value: never[]) => void; reject: (error: Error) => void }> = []
  const listConnections = vi.fn(() => new Promise<never[]>((resolve, reject) => completions.push({ resolve, reject })))
  const api = stubApi({ listConnections })
  const store = makeStore(api)
  Object.assign(store.connections, { loaded: true, hasSnapshot: snapshot, error: 'Offline' })
  Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, data: 'editRoute' in props ? [{ metadata: { name: 'item' }, spec: { connections: [] } }] : [] })
  const background = store.load('connections', { mode: 'background' })
  const view = await mountVue(Toolsets, { store, api, ...props })
  const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Retry')!
  expect(retry).toBeTruthy()
  expect(retry.disabled).toBe(false)
  retry.focus()
  retry.click()
  await settleVue()
  expect(document.activeElement).toBe(retry)
  expect(retry.getAttribute('aria-disabled')).toBe('true')
  expect(retry.getAttribute('aria-busy')).toBe('true')
  retry.click()
  expect(listConnections).toHaveBeenCalledTimes(1)
  completions[0].resolve([])
  await background
  await settleVue()
  expect(listConnections).toHaveBeenCalledTimes(2)
  expect(document.activeElement).toBe(retry)
  expect(retry.isConnected).toBe(true)
  expect(retry.getAttribute('aria-busy')).toBe('true')
  completions[1].reject(new Error('Still offline'))
  await settleVue()
  const currentRetry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Retry')!
  expect(document.activeElement).toBe(currentRetry)
  expect(currentRetry.getAttribute('aria-disabled')).toBeNull()
  expect(view.element.textContent).toContain('Tool connections could not be refreshed. The last loaded list remains available.')
  expect(listConnections).toHaveBeenCalledTimes(2)
  currentRetry.click()
  await settleVue()
  expect(document.activeElement).toBe(currentRetry)
  expect(view.element.textContent).toContain('Tool connections could not be refreshed. The last loaded list remains available.')
  expect(listConnections).toHaveBeenCalledTimes(3)
  completions[2].reject(new Error('Still offline'))
  await settleVue()
  expect(document.activeElement).toBe(currentRetry)
  view.unmount()
})

it.each([
  ['create', { createRoute: true }],
  ['collection first run', {}],
  ['detail', { editRoute: true, editName: 'item' }],
])('keeps the initial connection load focused on %s and hands off on success', async (_name, props) => {
  let finish!: (value: never[]) => void
  const api = stubApi({ listConnections: () => new Promise<never[]>(resolve => { finish = resolve }) })
  const store = makeStore(api)
  Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, data: 'editRoute' in props ? [{ metadata: { name: 'item' }, spec: { connections: [] } }] : [] })
  const view = await mountVue(Toolsets, { store, api, ...props })
  const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Load connections')!
  button.focus()
  button.click()
  await settleVue()
  expect(button.isConnected).toBe(true)
  expect(document.activeElement).toBe(button)
  expect(button.getAttribute('aria-busy')).toBe('true')
  finish([])
  await settleVue()
  expect(button.isConnected).toBe(false)
  expect(view.element.contains(document.activeElement)).toBe(true)
  view.unmount()
})

it('does not steal focus after the user moves it during a connection read', async () => {
  let finish!: (value: never[]) => void
  const api = stubApi({ listConnections: () => new Promise<never[]>(resolve => { finish = resolve }) })
  const store = makeStore(api)
  const view = await mountVue(Toolsets, { store, api, createRoute: true })
  const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Load connections')!
  button.click()
  await settleVue()
  const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
  input.focus()
  finish([])
  await settleVue()
  expect(document.activeElement).toBe(input)
  view.unmount()
})

it.each([
  ['create', { createRoute: true }],
  ['collection first run', {}],
  ['detail', { editRoute: true, editName: 'item' }],
])('hands focus to retry when the initial connection load fails on %s', async (_name, props) => {
  let fail!: (error: Error) => void
  const api = stubApi({ listConnections: () => new Promise<never[]>((_resolve, reject) => { fail = reject }) })
  const store = makeStore(api)
  Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, data: 'editRoute' in props ? [{ metadata: { name: 'item' }, spec: { connections: [] } }] : [] })
  const view = await mountVue(Toolsets, { store, api, ...props })
  const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Load connections')!
  button.focus()
  button.click()
  await settleVue()
  fail(new Error('Offline'))
  await settleVue()
  const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Retry')!
  expect(retry).toBeTruthy()
  expect(document.activeElement).toBe(retry)
  view.unmount()
})

it('does not steal focus after an initial connection load fails', async () => {
  let fail!: (error: Error) => void
  const api = stubApi({ listConnections: () => new Promise<never[]>((_resolve, reject) => { fail = reject }) })
  const store = makeStore(api)
  const view = await mountVue(Toolsets, { store, api, createRoute: true })
  const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Load connections')!
  button.click()
  await settleVue()
  const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
  input.focus()
  fail(new Error('Offline'))
  await settleVue()
  expect(document.activeElement).toBe(input)
  view.unmount()
})

it('does not restore focus after the authority changes during a failed read', async () => {
  let fail!: (error: Error) => void
  const api = stubApi({ listConnections: () => new Promise<never[]>((_resolve, reject) => { fail = reject }) })
  const store = makeStore(api)
  const view = await mountVue(Toolsets, { store, api, createRoute: true, authorityEpoch: 1 })
  const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Load connections')!
  button.focus()
  button.click()
  await settleVue()
  await view.setProps({ authorityEpoch: 2 })
  fail(new Error('Offline'))
  await settleVue()
  expect(document.activeElement).not.toBe(view.element.querySelector('.k-create-title'))
  view.unmount()
})
