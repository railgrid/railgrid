import { createApp, h, reactive, type App as VueApp } from 'vue'
import App from './App.vue'
import type { FarosContext } from './types'

const TAG = 'faros-provider-app-studio'

class ProjectsElement extends HTMLElement {
  private app: VueApp | null = null
  private host: HTMLDivElement | null = null
  private state = reactive<{ ctx: FarosContext | null }>({ ctx: null })

  set farosContext(v: FarosContext | null) {
    this.state.ctx = v
  }

  get farosContext(): FarosContext | null {
    return this.state.ctx
  }

  connectedCallback(): void {
    if (this.app) return
    this.style.display = 'block'
    this.style.height = '100%'
    this.style.width = '100%'
    this.style.minHeight = '0'
    this.host = document.createElement('div')
    this.host.className = 'h-full min-h-0 w-full'
    this.appendChild(this.host)
    this.app = createApp({
      render: () =>
        h(App, {
          ctx: this.state.ctx,
          navigate: (path: string) => this.navigate(path),
        }),
    })
    this.app.mount(this.host)
  }

  disconnectedCallback(): void {
    this.app?.unmount()
    this.app = null
    if (this.host?.parentNode === this) this.removeChild(this.host)
    this.host = null
  }

  private navigate(path: string): void {
    this.dispatchEvent(
      new CustomEvent('faros-navigate', {
        detail: { path },
        bubbles: true,
      }),
    )
  }
}

if (!customElements.get(TAG)) {
  customElements.define(TAG, ProjectsElement)
}
