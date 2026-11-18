import { nextTick, type Directive } from 'vue'

// The caller owns the recovery destination. Only move focus when removing the
// notice would otherwise strand the person who activated its action.
export const vFocusOnRecovery: Directive<HTMLElement, () => void> = {
  beforeUnmount(notice, { value: focusTarget }) {
    const doc = notice.ownerDocument
    const parent = notice.parentElement
    if (!notice.contains(doc.activeElement)) return
    void nextTick(() => {
      if (parent?.isConnected && doc.activeElement === doc.body) focusTarget()
    })
  },
}
