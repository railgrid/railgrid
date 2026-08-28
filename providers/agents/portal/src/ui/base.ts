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
import { hashFor, type Route } from '../router'

export class LightElement extends LitElement {
  protected createRenderRoot(): HTMLElement {
    return this
  }
}

// A confirmation is allowed to outlive the view that opened it. Keep the
// authority references together so callers can verify the mounted surface
// before starting a destructive write after the modal resolves.
export interface AuthoritySnapshot {
  readonly store: AppStore
  readonly api: ApiClient
  readonly host: HTMLElement | null
  readonly routeKey: string | null
}

interface AgentsHost extends HTMLElement {
  store?: AppStore
  api?: ApiClient
  route?: Route
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

  protected captureAuthority(): AuthoritySnapshot {
    const host = this.closest('faros-provider-agents') as AgentsHost | null
    return {
      store: this.store,
      api: this.api,
      host,
      routeKey: host?.route ? hashFor(host.route) : null,
    }
  }

  // A mounted child can still be connected for one turn after the shell has
  // rotated its context. Compare against the owning shell as well as the
  // child's properties so that same-tick token/user/tenant changes cannot
  // turn an old confirmation into a write against the new or old authority.
  protected authorityIsCurrent(snapshot: AuthoritySnapshot): boolean {
    if (!this.isConnected || this.store !== snapshot.store || this.api !== snapshot.api) return false
    const host = this.closest('faros-provider-agents') as AgentsHost | null
    if (host !== snapshot.host) return false
    if (!host) return true
    if (!host.isConnected || host.store !== snapshot.store || host.api !== snapshot.api) return false
    return (host.route ? hashFor(host.route) : null) === snapshot.routeKey
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
