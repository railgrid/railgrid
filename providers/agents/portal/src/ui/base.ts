// Base classes for every component in this bundle.
//
// The element runs in LIGHT DOM (createRenderRoot returns `this`) so the host
// portal's :root design tokens and the one namespaced stylesheet cascade in —
// exactly the property the previous innerHTML implementation relied on. Shadow
// DOM would isolate us from the host's theme.

import { LitElement } from 'lit'
import { property } from 'lit/decorators.js'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { Route } from '../router'

export class LightElement extends LitElement {
  protected createRenderRoot(): HTMLElement {
    return this
  }
}

// StoreElement is a light-DOM component wired to the shared AppStore: it
// re-renders on every store 'change' and re-binds if the store instance itself
// is swapped (tenant switch).
export class StoreElement extends LightElement {
  @property({ attribute: false }) store!: AppStore
  @property({ attribute: false }) api!: ApiClient

  private bound: AppStore | null = null
  private onStoreChange = (): void => this.requestUpdate()

  connectedCallback(): void {
    super.connectedCallback()
    this.bind()
  }

  disconnectedCallback(): void {
    this.bound?.removeEventListener('change', this.onStoreChange)
    this.bound = null
    super.disconnectedCallback()
  }

  protected willUpdate(): void {
    this.bind()
  }

  private bind(): void {
    if (this.bound === this.store || !this.store) return
    this.bound?.removeEventListener('change', this.onStoreChange)
    this.bound = this.store
    this.bound.addEventListener('change', this.onStoreChange)
  }

  // navigate asks the shell to change route. Bubbling a CustomEvent keeps
  // children decoupled from the router.
  protected navigate(route: Route): void {
    this.dispatchEvent(new CustomEvent<Route>('agents-navigate', { detail: route, bubbles: true, composed: true }))
  }
}
