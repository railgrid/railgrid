export interface RemoteWarehouse { id: string; name: string; state?: string; warehouseType?: string; supported: boolean; unsupported?: boolean; unsupportedReason?: string; reason?: string }
export interface RemoteCatalog { name: string; comment?: string; catalogType?: string; supported: boolean; unsupportedReason?: string; reason?: string }
export interface RemoteSchema { name: string; catalog: string; comment?: string; supported: boolean; unsupportedReason?: string; reason?: string }
export interface RemoteTable { name: string; catalog: string; schema: string; tableType?: string; dataSourceFormat?: string; comment?: string; supported: boolean; unsupported?: boolean; unsupportedReason?: string; reason?: string }
export interface RemotePage<T> { items: T[]; nextPageToken?: string }
export type RegistrationState = 'created' | 'existing' | 'conflict' | 'failed'
export interface RegistrationResult { index: number; name?: string; state: RegistrationState; message?: string }
export interface RegistrationItem { name: string; warehouseID?: string; catalog?: string; schema?: string; table?: string }

export type InitializationPhase = 'idle' | 'loading' | 'success' | 'error'
export type InitializationResource = 'connections' | 'warehouses' | 'tables'
export type InitializationState = Record<InitializationResource, InitializationPhase>

export interface DiscoveryCoordinates {
  connectionRef: string
  warehouseRef: string
  catalog: string
  schema: string
}

export type DiscoveryLevel = 'warehouses' | 'catalogs' | 'schemas' | 'tables'
