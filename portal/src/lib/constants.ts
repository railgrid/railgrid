export const API_PATHS = {
  healthz: '/healthz',
  version: '/version',
  tokenLogin: '/auth/token-login',
  authorize: '/auth/authorize',
} as const

export const STORAGE_KEYS = {
  auth: 'faros-auth',
} as const
