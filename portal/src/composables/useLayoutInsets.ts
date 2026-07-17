import { reactive, readonly } from 'vue'

// Shared, app-lifetime insets describing how much room the nav chrome
// (side/bottom docks) occupies. AppLayout is the single writer; the
// persistent TerminalDock (mounted at the app root, *outside* AppLayout's
// DOM subtree) is a reader. A plain reactive singleton is used instead of
// CSS custom properties because the dock lives above the router-view
// boundary and can't reliably inherit vars set inside a per-page component.
interface LayoutInsets {
  left: string
  right: string
  bottom: string
}

const state = reactive<LayoutInsets>({
  left: '0px',
  right: '0px',
  bottom: '0px',
})

export function setLayoutInsets(next: LayoutInsets) {
  state.left = next.left
  state.right = next.right
  state.bottom = next.bottom
}

export function useLayoutInsets() {
  return readonly(state)
}
