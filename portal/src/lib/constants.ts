export const API_PATHS = {
  healthz: '/healthz',
  tokenLogin: '/auth/token-login',
  authorize: '/auth/authorize',
  graphql: (clusterName: string) => `/graphql/${clusterName}`,
} as const

export const STORAGE_KEYS = {
  auth: 'kedge-auth',
} as const
