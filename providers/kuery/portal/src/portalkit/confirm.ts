// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`. The Vue provider portals build self-contained (no npm
// workspace), so this kit is copy-synced rather than imported.
//
// Promise-based confirm dialog, replacing the browser's native window.confirm so
// destructive actions use an in-app modal that matches the portal's styling.
// A single <ConfirmDialog> (mounted once in App.vue) renders this shared state;
// call sites await confirmDialog(...) and get true/false.
import { reactive } from 'vue'

export interface ConfirmOptions {
  title: string
  // message is optional supporting text shown under the title; it may contain
  // newlines, which the dialog renders as paragraph breaks.
  message?: string
  confirmLabel?: string
  cancelLabel?: string
  // danger styles the confirm button as a solid destructive action (delete/remove).
  danger?: boolean
}

interface ConfirmState extends Required<Omit<ConfirmOptions, 'message'>> {
  open: boolean
  message: string
  resolve: ((ok: boolean) => void) | null
}

export const confirmState = reactive<ConfirmState>({
  open: false,
  title: '',
  message: '',
  confirmLabel: 'Confirm',
  cancelLabel: 'Cancel',
  danger: false,
  resolve: null,
})

// confirmDialog opens the modal and resolves true when the user confirms, false
// on cancel/escape/backdrop. If a dialog is somehow already open, its promise is
// resolved false before the new one replaces it.
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  if (confirmState.resolve) {
    confirmState.resolve(false)
    confirmState.resolve = null
  }
  confirmState.title = opts.title
  confirmState.message = opts.message ?? ''
  confirmState.confirmLabel = opts.confirmLabel ?? 'Confirm'
  confirmState.cancelLabel = opts.cancelLabel ?? 'Cancel'
  confirmState.danger = opts.danger ?? false
  confirmState.open = true
  return new Promise<boolean>(resolve => {
    confirmState.resolve = resolve
  })
}

// resolveConfirm closes the dialog and settles the pending promise.
export function resolveConfirm(ok: boolean): void {
  confirmState.open = false
  const resolve = confirmState.resolve
  confirmState.resolve = null
  if (resolve) resolve(ok)
}
