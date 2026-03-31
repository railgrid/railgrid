export interface LoginResponse {
  kubeconfig?: string
  expiresAt?: number
  email?: string
  userId?: string
  idToken?: string
  refreshToken?: string
  issuerUrl?: string
  clientId?: string
}

export interface StoredAuth {
  idToken: string
  refreshToken?: string
  expiresAt: number
  issuerUrl?: string
  clientId?: string
  email: string
  userId: string
  clusterName: string
}

export type AuthMode = 'oidc' | 'token' | 'both'

export interface HealthzResponse {
  status: string
  oidc: boolean
  issuerUrl?: string
  clientId?: string
}
