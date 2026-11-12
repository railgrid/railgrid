import { ensureRailgridUIStyles, RAILGRID_UI_CORE_VERSION } from '../../provider-sdk/portalkit/styles'

const stale = document.createElement('style')
stale.id = 'design-review-stale-host'
stale.textContent = ':root { --railgrid-ui-canonical: 1; --railgrid-ui-core-version: 1; }'
document.head.append(stale)
ensureRailgridUIStyles()
const actual = getComputedStyle(document.documentElement).getPropertyValue('--railgrid-ui-core-version').trim()
const preserved = document.getElementById(stale.id) === stale
document.querySelector('[data-result]').textContent = 'Stale host ' + (preserved ? 'preserved' : 'replaced') + ' · computed fallback version ' + actual + ' · expected ' + RAILGRID_UI_CORE_VERSION
