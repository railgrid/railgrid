export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  orgUUID?: string | null
  workspaceUUID?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
  subPath?: string
}

export interface ErrorResponse {
  reason: string
  message: string
}

export interface ConditionInfo {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export type AuthType = 'pat'

export interface Connection {
  name: string
  uid?: string
  host: string
  authType: AuthType
  secretName: string
  secretNamespace: string
  secretKey: string
  workspaceID?: string
  generation?: number
  observedGeneration?: number
  creationTimestamp?: string
  status: string
  message?: string
  conditions: ConditionInfo[]
}

export interface Warehouse {
  name: string
  connectionRef: string
  warehouseID: string
  state?: string
  generation?: number
  observedGeneration?: number
  creationTimestamp?: string
  status: string
  message?: string
  conditions: ConditionInfo[]
}

export interface TableColumn {
  name: string
  type: string
  nullable?: boolean
  comment?: string
}

export interface Table {
  name: string
  connectionRef: string
  warehouseRef: string
  catalog: string
  schema: string
  table: string
  fullName: string
  refreshedAt?: string
  generation?: number
  observedGeneration?: number
  creationTimestamp?: string
  columns: TableColumn[]
  status: string
  message?: string
  conditions: ConditionInfo[]
}
