import { API_PATHS } from './constants'
import type { HealthzResponse, LoginResponse } from '@/auth/types'

export async function fetchHealthz(): Promise<HealthzResponse> {
  const res = await fetch(API_PATHS.healthz)
  if (!res.ok) throw new Error(`healthz failed: ${res.status}`)
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
