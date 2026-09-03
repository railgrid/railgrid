import { createApp, h, reactive } from 'vue'
import DashboardTile from './DashboardTile.vue'
import { ensureAppStudioStyles } from './styles'
import type { FarosContext } from './types'

export function mount(element: HTMLElement, context: FarosContext | null) {
  ensureAppStudioStyles()
  const state = reactive<{ ctx: FarosContext | null }>({ ctx: context })
  const host = document.createElement('div')
  host.className = 'app-studio-tile-host'
  element.replaceChildren()
  element.appendChild(host)

  const app = createApp({
    render: () => h(DashboardTile, { context: state.ctx }),
  })
  app.mount(host)

  return {
    setContext(value: FarosContext | null) {
      state.ctx = value
    },
    unmount() {
      app.unmount()
      if (host.parentNode === element) element.removeChild(host)
    },
  }
}
