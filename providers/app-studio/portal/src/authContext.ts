import type { RailgridContext } from './types'

/** True only when the host supplied an authenticated request transport. */
export function hasProviderTransport(ctx: RailgridContext | null | undefined): boolean {
  return Boolean(ctx && (typeof ctx.fetch === 'function' || ctx.token?.trim()))
}

/** Copy only the transport fields a trusted host actually supplied. */
export function providerTransportFields(ctx: RailgridContext | null | undefined): Pick<RailgridContext, 'fetch' | 'token'> {
  if (!ctx) return {}
  return {
    ...(typeof ctx.fetch === 'function' ? { fetch: ctx.fetch } : {}),
    ...(ctx.token?.trim() ? { token: ctx.token } : {}),
  }
}

/**
 * A view identity key for async work and local state. Host fetch transports
 * own credential rotation, so their bearer never belongs in this key. Legacy
 * hosts still use the bearer as the authority-change signal.
 */
export function providerContextFingerprint(ctx: RailgridContext | null | undefined): string {
  if (!ctx) return ''
  const user = ctx.user
  return JSON.stringify([
    hasProviderTransport(ctx),
    ctx.tenant ?? '',
    ctx.orgUUID ?? '',
    ctx.workspaceUUID ?? '',
    user?.userId ?? '',
    user?.sub ?? '',
    user?.email ?? '',
    typeof ctx.fetch === 'function' ? '' : ctx.token?.trim() ?? '',
  ])
}
