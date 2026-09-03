import {
  clearDatabricksReturnIntent,
  databricksJourneyTenantKey,
  databricksJourneyStorage,
  destinationAfterPrerequisite,
  firstRunModel,
  readDatabricksPrerequisiteIntent,
  prerequisiteCreatePath,
  readDatabricksReturnIntent,
  writeDatabricksPrerequisiteIntent,
  writeDatabricksReturnIntent,
  type DatabricksJourneyStorage,
} from './journey.js'

function equal(actual: unknown, expected: unknown, label: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${label}: ${JSON.stringify(actual)}`)
  }
}

equal(firstRunModel('connection', false, false).primary, { label: 'Create connection', action: 'create-connection' }, 'connection empty state starts the journey')
equal(firstRunModel('warehouse', false, false).currentStep, 0, 'warehouse empty state points to connection')
equal(firstRunModel('warehouse', true, false).primary.action, 'browse-warehouses', 'warehouse empty state offers discovery')
equal(firstRunModel('table', false, false).primary.action, 'create-connection', 'table journey starts with connection')
equal(firstRunModel('table', true, false).primary.action, 'browse-warehouses', 'table journey advances to warehouse')
equal(firstRunModel('table', true, true).primary.action, 'browse-tables', 'table journey reaches discovery')
equal(prerequisiteCreatePath('connection'), 'create/connection', 'connection prerequisite path')
equal(prerequisiteCreatePath('warehouse'), 'create/warehouse/browse', 'warehouse prerequisite path')
equal(destinationAfterPrerequisite('connection', 'create/warehouse/browse'), { path: 'create/warehouse/browse', keepReturnIntent: false }, 'warehouse journey resumes after connection')
equal(destinationAfterPrerequisite('connection', 'create/table/browse'), { path: 'create/warehouse/browse', keepReturnIntent: true }, 'table journey advances through warehouse')
equal(destinationAfterPrerequisite('connection', 'create/table/manual'), { path: 'create/warehouse/browse', keepReturnIntent: true }, 'manual table journey preserves its original mode through warehouse setup')
equal(destinationAfterPrerequisite('warehouse', 'create/table/manual'), { path: 'create/table/manual', keepReturnIntent: false }, 'table manual entry resumes after warehouse')

const values = new Map<string, string>()
const storage: DatabricksJourneyStorage = {
  getItem: key => values.get(key) ?? null,
  setItem: (key, value) => { values.set(key, value) },
  removeItem: key => { values.delete(key) },
}
const tenantA = databricksJourneyTenantKey('root:tenant-a', 'org-a', 'workspace-a')
const tenantB = databricksJourneyTenantKey('root:tenant-b', 'org-b', 'workspace-b')
writeDatabricksPrerequisiteIntent(storage, tenantA, 'tables', 'create/table/browse', 'create/warehouse/browse')
equal(readDatabricksPrerequisiteIntent(storage, tenantA, 'create/warehouse/browse'), { originPath: 'tables', successPath: 'create/table/browse' }, 'prerequisite intent keeps cancel origin separate from success destination')
writeDatabricksReturnIntent(storage, tenantA, 'create/table/browse', 'create/connection')
equal(readDatabricksReturnIntent(storage, tenantA, 'create/connection'), 'create/table/browse', 'same tenant and prerequisite route restore return intent')
writeDatabricksReturnIntent(storage, tenantA, 'create/table/browse', 'create/warehouse/browse')
equal(readDatabricksReturnIntent(storage, tenantA, 'create/warehouse/browse'), 'create/table/browse', 'two-hop journey advances its active prerequisite without replacing the table return path')
writeDatabricksReturnIntent(storage, tenantA, 'create/table/browse', 'create/connection')
equal(readDatabricksReturnIntent(storage, tenantB, 'create/connection'), null, 'another tenant cannot restore return intent')
equal(readDatabricksReturnIntent(storage, tenantA, 'create/connection'), 'create/table/browse', 'reading another tenant does not consume the active tenant intent')
writeDatabricksReturnIntent(storage, tenantA, 'create/table/browse', 'create/connection')
equal(readDatabricksReturnIntent(storage, tenantA, 'connections'), null, 'leaving the prerequisite route clears stale intent')
writeDatabricksReturnIntent(storage, tenantA, 'create/warehouse/manual', 'create/connection')
clearDatabricksReturnIntent(storage)
equal(readDatabricksReturnIntent(storage, tenantA, 'create/connection'), null, 'normal navigation clears return intent')
values.set('faros:databricks:return-intent', JSON.stringify({ tenantKey: tenantA, returnPath: 'connections', expectedPath: 'create/connection' }))
equal(readDatabricksReturnIntent(storage, tenantA, 'create/connection'), null, 'invalid return paths are rejected')
values.set('faros:databricks:return-intent', JSON.stringify({ tenantKey: tenantA, returnPath: 'create/table/browse', expectedPath: 'create/connection' }))
equal(readDatabricksPrerequisiteIntent(storage, tenantA, 'create/connection'), { originPath: 'tables', successPath: 'create/table/browse' }, 'legacy single-record intent derives its collection origin')

const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
Object.defineProperty(globalThis, 'window', {
  configurable: true,
  get: () => { throw new Error('sessionStorage blocked') },
})
try {
  equal(databricksJourneyStorage(), null, 'blocked sessionStorage getter degrades without preventing provider startup')
} finally {
  if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor)
  else Reflect.deleteProperty(globalThis, 'window')
}
