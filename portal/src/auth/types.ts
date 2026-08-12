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
  // Whether the hub offers interactive static-token login. Absent on older
  // hubs, which always offered it when reachable — treat undefined as true.
  tokenLogin?: boolean
  issuerUrl?: string
  clientId?: string
}

export interface VersionResponse {
  version: string
  gitCommit: string
  buildDate: string
}

export interface BrowserSessionResponse {
  authenticated: boolean
  userId?: string
  email?: string
  name?: string
  expiresAt?: number
}
