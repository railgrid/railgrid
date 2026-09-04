import type { ToastKind } from './portalkit/toast'

export interface ToastNotice {
  kind: ToastKind
  message: string
}

export type AccessSurface = 'production' | 'preview'
export type AccessMutation = 'grant' | 'invite' | 'revoke'

export function productionAccessUpdateToast(): ToastNotice {
  return {
    kind: 'info',
    message: 'Production access update accepted. Status will update here.',
  }
}

export function previewAccessUpdateToast(converged: boolean): ToastNotice {
  return converged
    ? { kind: 'ok', message: 'Preview access updated.' }
    : { kind: 'info', message: 'Preview access update accepted. Status will update here.' }
}

export function accessMutationToast(
  surface: AccessSurface,
  mutation: AccessMutation,
  subject = '',
): ToastNotice {
  const label = surface === 'production' ? 'Production' : 'Preview'
  const normalizedSubject = subject.trim()
  if (mutation === 'revoke') return { kind: 'ok', message: `${label} access revoked.` }
  if (mutation === 'invite') {
    return {
      kind: 'ok',
      message: normalizedSubject
        ? `${label} invitation created for ${normalizedSubject}.`
        : `${label} invitation created.`,
    }
  }
  return {
    kind: 'ok',
    message: normalizedSubject
      ? `${label} access granted to ${normalizedSubject}.`
      : `${label} access granted.`,
  }
}
