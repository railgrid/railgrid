// Provider-local stand-in for @/stores/terminalSessions, bridging to
// the portal's global TerminalDock via a CustomEvent. Same shape as
// kubernetes-edges' terminal-adapter; duplicated so each provider's
// Pinia has its own store ID.

import { defineStore } from 'pinia'

interface OpenSessionParams {
  edgeName: string
  cluster: string
  displayName?: string
  forceNew?: boolean
}

export const useTerminalSessionsStore = defineStore('server-edges-provider-terminal', {
  state: () => ({}),
  actions: {
    openSession(params: OpenSessionParams) {
      window.dispatchEvent(
        new CustomEvent('kedge-terminal-open', {
          detail: params,
        }),
      )
    },
  },
})
