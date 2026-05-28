// Provider-local stand-in for @/stores/terminalSessions.
//
// The portal's terminal-dock store manages a global UI dock that lives
// OUTSIDE this provider's Vue app, so we can't reach its state from
// here. We bridge via a CustomEvent ("kedge-terminal-open") that the
// portal's TerminalDock listens for; the dock decodes the detail and
// runs its existing openSession path.
//
// API surface is intentionally narrow — only openSession is wired,
// since that's the only entrypoint EdgeDetailPage currently uses from
// the provider. If a provider needs additional terminal-store
// operations later, extend both the adapter and the bridge listener.

import { defineStore } from 'pinia'

interface OpenSessionParams {
  edgeName: string
  cluster: string
  displayName?: string
  forceNew?: boolean
}

export const useTerminalSessionsStore = defineStore('kubernetes-edges-provider-terminal', {
  state: () => ({}),
  actions: {
    openSession(params: OpenSessionParams) {
      // Document scope is enough — bubbles up out of the custom element's
      // light DOM and reaches the portal's TerminalDock listener.
      window.dispatchEvent(
        new CustomEvent('kedge-terminal-open', {
          detail: params,
        }),
      )
    },
  },
})
