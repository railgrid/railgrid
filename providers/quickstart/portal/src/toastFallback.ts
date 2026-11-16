import { createApp, h } from 'vue'
import ToastHost from './portalkit/ToastHost.vue'

// Keep the host outside the element's innerHTML render target.
export function mountToastFallback(): () => void {
  const node = document.createElement('div')
  document.body.appendChild(node)
  const app = createApp({ render: () => h(ToastHost, { owner: 'fallback' }) })
  app.mount(node)
  return () => { app.unmount(); node.remove() }
}
