import { reactive } from 'vue'

export interface ConfirmOptions {
  title: string
  message: string
  confirmLabel?: string
}

interface ConfirmState extends Required<ConfirmOptions> {
  open: boolean
  resolve: ((ok: boolean) => void) | null
}

export const confirmState = reactive<ConfirmState>({
  open: false,
  title: '',
  message: '',
  confirmLabel: 'Delete',
  resolve: null,
})

export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  if (confirmState.resolve) {
    confirmState.resolve(false)
    confirmState.resolve = null
  }
  confirmState.title = opts.title
  confirmState.message = opts.message
  confirmState.confirmLabel = opts.confirmLabel ?? 'Delete'
  confirmState.open = true
  return new Promise(resolve => {
    confirmState.resolve = resolve
  })
}

export function resolveConfirm(ok: boolean): void {
  confirmState.open = false
  const resolve = confirmState.resolve
  confirmState.resolve = null
  if (resolve) resolve(ok)
}
