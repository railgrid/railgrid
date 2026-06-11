// Provider-local bridge for downloading an edge-scoped kubeconfig.
//
// Generating the kubeconfig requires the hub's OIDC config plus the
// portal's active org/ws UUIDs and bearer token — none of which live
// inside this isolated micro-frontend (the kedgeContext only carries the
// token, cluster name, and user). So we bridge via a CustomEvent
// ("kedge-edge-kubeconfig-download") that the portal's ProviderFrame
// listens for; it relays the edge name to the tenant store's
// downloadEdgeKubeconfig, which knows the org/ws and triggers the file
// download. Mirror of terminal-adapter.ts's kedge-terminal-open pattern.

import { defineStore } from 'pinia'

export const useKubeconfigStore = defineStore('kubernetes-edges-provider-kubeconfig', {
  state: () => ({}),
  actions: {
    downloadEdgeKubeconfig(edgeName: string) {
      // Window scope clears the custom element's light DOM and reaches
      // the portal's ProviderFrame listener.
      window.dispatchEvent(
        new CustomEvent('kedge-edge-kubeconfig-download', {
          detail: { edgeName },
        }),
      )
    },
  },
})
