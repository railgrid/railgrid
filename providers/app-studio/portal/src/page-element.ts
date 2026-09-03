import { createApp, h, reactive } from 'vue'
import App from './App.vue'
import { ensureAppStudioStyles } from './styles'
import type { FarosContext } from './types'

type NavigationOptions = { replace?: boolean }

export function mount(element: HTMLElement, context: FarosContext | null) {
  ensureAppStudioStyles()
  const state = reactive<{ ctx: FarosContext | null }>({ ctx: context })

  element.style.display = 'block'
  element.style.height = '100%'
  element.style.width = '100%'
  element.style.minHeight = '0'

  const host = document.createElement('div')
  host.className = 'h-full min-h-0 w-full'
  // Keep Vue Teleport targets inside the provider element. App Studio's
  // Tailwind bundle is scoped to this custom element; a body target would
  // render overlays outside that scope and lose the provider utilities.
  const overlayRoot = document.createElement('div')
  overlayRoot.id = 'app-studio-overlay-root'
  overlayRoot.className = 'app-studio-overlay-root'
  element.replaceChildren()
  element.appendChild(host)
  element.appendChild(overlayRoot)

  const navigate = (path: string, options: NavigationOptions = {}) => {
    element.dispatchEvent(
      new CustomEvent('faros-navigate', {
        detail: { path, ...(options.replace === true ? { replace: true } : {}) },
        bubbles: true,
      }),
    )
  }

  const requestFullBleed = (fullBleed: boolean) => {
    element.dispatchEvent(
      new CustomEvent('faros-layout-change', {
        detail: { fullBleed },
        bubbles: true,
      }),
    )
  }

  const app = createApp({
    render: () =>
      h(App, {
        ctx: state.ctx,
        navigate,
        requestFullBleed,
      }),
  })
  app.mount(host)

  return {
    setContext(value: FarosContext | null) {
      state.ctx = value
    },
    unmount() {
      app.unmount()
      if (host.parentNode === element) element.removeChild(host)
      if (overlayRoot.parentNode === element) element.removeChild(overlayRoot)
    },
  }
}
