import { createApp, defineComponent, h, nextTick, shallowReactive, type App, type Component } from 'vue'
import type { Route } from '../router'

const mountedApps = new Set<() => void>()

export interface MountedVue {
  app: App
  element: HTMLElement
  navigations: Route[]
  events: Record<string, unknown[]>
  props: Record<string, unknown>
  setProps: (next: Record<string, unknown>) => Promise<void>
  unmount: () => void
}

export async function settleVue(passes = 4, delayMS = 0): Promise<void> {
  for (let index = 0; index < passes; index += 1) {
    await nextTick()
    await Promise.resolve()
  }
  if (delayMS > 0) {
    await new Promise(resolve => window.setTimeout(resolve, delayMS))
    await nextTick()
  }
}

export async function mountVue(component: Component, initialProps: Record<string, unknown>): Promise<MountedVue> {
  const props = shallowReactive<Record<string, unknown>>({ ...initialProps })
  const navigations: Route[] = []
  const events: Record<string, unknown[]> = {}
  const capture = (name: string) => (detail?: unknown) => {
    ;(events[name] ||= []).push(detail)
  }
  const componentEmits = (component as { emits?: string[] | Record<string, unknown> }).emits
  const declared = new Set(Array.isArray(componentEmits) ? componentEmits : Object.keys(componentEmits || {}))
  const listeners: Record<string, (detail?: unknown) => void> = {}
  if (declared.has('navigate')) listeners.onNavigate = route => navigations.push(route as Route)
  if (declared.has('create-success')) listeners.onCreateSuccess = capture('create-success')
  if (declared.has('create-cancel')) listeners.onCreateCancel = capture('create-cancel')
  if (declared.has('edit-success')) listeners.onEditSuccess = capture('edit-success')
  if (declared.has('edit-cancel')) listeners.onEditCancel = capture('edit-cancel')
  const element = document.createElement('div')
  document.body.appendChild(element)
  const app = createApp(defineComponent({
    setup() {
      return () => h(component, {
        ...props,
        ...listeners,
      })
    },
  }))
  app.mount(element)
  let unmounted = false
  const unmount = () => {
    if (unmounted) return
    unmounted = true
    mountedApps.delete(unmount)
    app.unmount()
  }
  mountedApps.add(unmount)
  await settleVue()
  return {
    app,
    element,
    navigations,
    events,
    props,
    setProps: async next => {
      Object.assign(props, next)
      await settleVue()
    },
    unmount,
  }
}

export function unmountVueApps(): void {
  for (const unmount of [...mountedApps]) unmount()
}

export function text(element: Element | null | undefined): string {
  return (element?.textContent || '').replace(/\s+/g, ' ').trim()
}
