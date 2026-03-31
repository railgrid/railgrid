import { STORAGE_KEYS } from '@/lib/constants'
import type { StoredAuth } from './types'

const EXPIRY_BUFFER_SECONDS = 30

export function loadAuth(): StoredAuth | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.auth)
    if (!raw) return null
    return JSON.parse(raw) as StoredAuth
  } catch {
    return null
  }
}

export function saveAuth(auth: StoredAuth): void {
  localStorage.setItem(STORAGE_KEYS.auth, JSON.stringify(auth))
}

export function clearAuth(): void {
  localStorage.removeItem(STORAGE_KEYS.auth)
}

export function isExpired(auth: StoredAuth): boolean {
  if (!auth.expiresAt) return false
  const now = Math.floor(Date.now() / 1000)
  return now > auth.expiresAt - EXPIRY_BUFFER_SECONDS
}

export async function refreshToken(auth: StoredAuth): Promise<StoredAuth | null> {
  if (!auth.refreshToken || !auth.issuerUrl || !auth.clientId) return null

  try {
    // Discover token endpoint from OIDC provider
    const discoveryRes = await fetch(`${auth.issuerUrl}/.well-known/openid-configuration`)
    if (!discoveryRes.ok) return null
    const discovery = await discoveryRes.json()
    const tokenEndpoint = discovery.token_endpoint as string

    // Refresh using public client (no client_secret, matches PKCE pattern)
    const body = new URLSearchParams({
      grant_type: 'refresh_token',
      refresh_token: auth.refreshToken,
      client_id: auth.clientId,
      scope: 'openid profile email offline_access',
    })

    const res = await fetch(tokenEndpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body,
    })
    if (!res.ok) return null

    const data = await res.json()
    const idToken = data.id_token as string
    const refreshTokenNew = (data.refresh_token as string) || auth.refreshToken
    const expiresIn = data.expires_in as number

    const updated: StoredAuth = {
      ...auth,
      idToken,
      refreshToken: refreshTokenNew,
      expiresAt: Math.floor(Date.now() / 1000) + expiresIn,
    }
    saveAuth(updated)
    return updated
  } catch {
    return null
  }
}

/** Extract cluster name from kubeconfig server URL (e.g. /clusters/{name}) */
export function parseClusterName(kubeconfig: string): string {
  const match = kubeconfig.match(/\/clusters\/([^\s/"]+)/)
  return match?.[1] ?? ''
}
