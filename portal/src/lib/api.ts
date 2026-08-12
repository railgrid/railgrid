import { API_PATHS } from './constants'
import type { BrowserSessionResponse, HealthzResponse, LoginResponse, VersionResponse } from '@/auth/types'

export async function fetchHealthz(): Promise<HealthzResponse> {
  const res = await fetch(API_PATHS.healthz)
  if (!res.ok) throw new Error(`healthz failed: ${res.status}`)
  return res.json()
}

export async function fetchVersion(): Promise<VersionResponse> {
  const res = await fetch(API_PATHS.version)
  if (!res.ok) throw new Error(`version failed: ${res.status}`)
  return res.json()
}

export async function loginWithToken(token: string): Promise<LoginResponse> {
  const res = await fetch(API_PATHS.tokenLogin, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`token login failed: ${res.status}`)
  return res.json()
}

// Establishes the hub-wide HttpOnly browser session from a portal bearer.
// The bearer is sent only to the same-origin hub and is never returned by the
// endpoint or made available to a published app.
export async function bootstrapBrowserSession(token: string): Promise<BrowserSessionResponse> {
  const res = await fetch('/auth/session/bootstrap', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`browser session bootstrap failed: ${res.status}`)
  return res.json()
}
