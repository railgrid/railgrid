import { collectionPath, createPath, detailPath, parseSubPath, tableEditPath } from './route.js'
import { navigationDetail } from './navigation.js'

function equal(actual: unknown, expected: unknown, label: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${label}: ${JSON.stringify(actual)}`)
  }
}

equal(parseSubPath(undefined), { page: 'connections' }, 'bare provider path')
equal(parseSubPath('/create/connection/'), { page: 'create', kind: 'connection', mode: 'manual' }, 'connection create path is provider-relative')
equal(parseSubPath('create/warehouse/manual'), { page: 'create', kind: 'warehouse', mode: 'manual' }, 'manual warehouse path')
equal(parseSubPath('create/warehouse/browse'), { page: 'create', kind: 'warehouse', mode: 'browse' }, 'browse warehouse path')
equal(parseSubPath('create/table/manual'), { page: 'create', kind: 'table', mode: 'manual' }, 'manual table path')
equal(parseSubPath('create/table/browse'), { page: 'create', kind: 'table', mode: 'browse' }, 'browse table path')
equal(parseSubPath('providers/databricks/create/table/manual'), { page: 'connections' }, 'shell prefix is not part of provider subPath')
equal(parseSubPath('tables/orders%2Fhistory'), { page: 'tables', table: 'orders/history' }, 'detail name decoding')
equal(parseSubPath('tables/orders%2Fhistory/edit'), { page: 'tables', table: 'orders/history', edit: true }, 'table edit path decodes the encoded name')
equal(parseSubPath('tables/orders/edit/extra'), { page: 'tables', table: 'orders/edit/extra' }, 'malformed table edit path remains a detail path')
equal(createPath('connection'), 'create/connection', 'connection path stays relative')
equal(createPath('warehouse', 'browse'), 'create/warehouse/browse', 'warehouse browse path')
equal(createPath('table', 'manual'), 'create/table/manual', 'table manual path')
equal(collectionPath('warehouses'), 'warehouses', 'collection path')
equal(detailPath('tables', 'orders history'), 'tables/orders%20history', 'detail path encoding')
equal(tableEditPath('orders history'), 'tables/orders%20history/edit', 'table edit path encoding')
equal(navigationDetail('warehouses'), { path: 'warehouses' }, 'push navigation detail')
equal(navigationDetail('/warehouses'), { path: 'warehouses' }, 'navigation detail remains provider-relative')
equal(navigationDetail('warehouses', true), { path: 'warehouses', replace: true }, 'replace navigation detail')
