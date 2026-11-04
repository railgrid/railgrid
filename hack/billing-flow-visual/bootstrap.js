// This entry runs only on the dedicated, loopback-only preview origin.
import { ORG, WS } from './fixtures.mjs'
const query = new URLSearchParams(location.search)
document.cookie = `railgrid-billing-fixture=${crypto.randomUUID()}; path=/; SameSite=Strict`
localStorage.setItem('railgrid-auth', JSON.stringify({ idToken: 'design-fixture-only', userId: 'fixture-user', email: 'fixture@example.test', expiresAt: Math.floor(Date.now() / 1000) + 3600, clusterName: 'abc123def456' }))
localStorage.setItem('railgrid:portal:tenant', JSON.stringify({ orgUUID: ORG, workspaceUUID: WS, workspaceMode: 'workspace' }))
localStorage.setItem('railgrid-sidebar-expanded', innerWidth > 768 ? '1' : '0')
if (new URLSearchParams(location.search).has('previewTheme')) localStorage.setItem('railgrid-theme', new URLSearchParams(location.search).get('previewTheme'))
// Keep scenario selection outside production UI. No production component is
// replaced or taught about the fixture's preview query parameter.
function tools() {
  const bar = document.createElement('div')
  bar.setAttribute('data-billing-fixture-tools', '')
  Object.assign(bar.style, { position: 'fixed', bottom: '8px', right: '12px', zIndex: '2147482999', padding: '8px', background: 'var(--color-surface-raised)', color: 'var(--color-text-secondary)', border: '1px solid var(--color-border-default)', borderRadius: '4px', font: '11px var(--font-sans)', maxWidth: 'calc(100vw - 24px)', display: 'flex', gap: '8px', alignItems: 'center' })
  const label = document.createElement('span')
  label.textContent = 'API fixture · $50/mo is illustrative'
  const picker = document.createElement('select')
  picker.className = 'k-input'
  picker.style.cssText = 'width:auto;font-size:11px;max-width:150px;padding:4px 8px'
  picker.setAttribute('aria-label', 'Billing fixture scenario')
  for (const [value, text] of [['plans', 'Discovery'], ['review', 'Review'], ['failed', 'Checkout failure'], ['active', 'Active subscription'], ['unavailable', 'Price unavailable'], ['operator', 'Admin managed'], ['override', 'Custom allowance'], ['member', 'Member request']]) {
    picker.add(new Option(text, value, false, query.get('preview') === value))
  }
  picker.addEventListener('change', () => {
    const next = new URL(location.href)
    next.search = new URLSearchParams({ panel: 'billing', preview: picker.value, ...(picker.value === 'plans' ? {} : { review: '1' }), ...(picker.value === 'override' ? { meter: 'workspaces' } : {}) }).toString()
    location.assign(next)
  })
  bar.append(label, picker)
  document.body.append(bar)
}
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', tools, { once: true })
else tools()
