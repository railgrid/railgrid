import { expect, it, vi } from 'vitest'

it.each([
  ['ready', 'Connect an AI client'],
  ['pending', 'Token is still being provisioned.'],
  ['connect-failure', 'connect: 503'],
  ['empty', 'No providers discovered for this endpoint.'],
])('mounts the real fixture entry for %s and navigates from collection to detail', async (scenario, expected) => {
  vi.resetModules()
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  localStorage.clear()
  window.history.replaceState({}, '', `/mcp-access.html?view=collection&scenario=${scenario}`)
  document.body.innerHTML = '<div id="app"></div>'
  const fixture = await import('./mcp-access.js')
  await fixture.mounted
  try {
    await vi.waitFor(() => expect(document.querySelector('[data-mcp-route-surface="collection"]')?.textContent).toContain('operations'))
    const button = [...document.querySelectorAll('button')].find(el => el.textContent?.trim() === 'operations')
    expect(button).toBeTruthy()
    button!.click()
    await vi.waitFor(() => expect(document.body.textContent).toContain(expected))
    if (scenario === 'ready') {
      expect(document.body.textContent).toContain('Edge services')
      expect(document.body.textContent).toContain('Code')
    }
  } finally {
    fixture.app.unmount()
    vi.unstubAllGlobals()
  }
})

it('creates a server and opens the accepted resource in the real fixture', async () => {
  vi.resetModules()
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  localStorage.clear()
  window.history.replaceState({}, '', '/mcp-access.html?view=collection')
  document.body.innerHTML = '<div id="app"></div>'
  const fixture = await import('./mcp-access.js')
  await fixture.mounted
  try {
    await vi.waitFor(() => expect(document.body.textContent).toContain('operations'))
    ;[...document.querySelectorAll('button')].find(el => el.textContent?.includes('New server'))!.click()
    const input = document.querySelector<HTMLInputElement>('input[placeholder="ops"]')!
    await vi.waitFor(() => expect(input.offsetParent === null && input.closest('[style*="display: none"]')).toBeFalsy())
    input.value = 'new-server'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => expect([...document.querySelectorAll('button')].find(el => el.textContent?.trim() === 'Create server')?.disabled).toBe(false))
    ;[...document.querySelectorAll('button')].find(el => el.textContent?.trim() === 'Create server')!.click()
    await vi.waitFor(() => expect(fixture.router.currentRoute.value.params.name).toBe('new-server'))
    await vi.waitFor(() => expect(document.body.textContent).toContain('Connect an AI client'))
  } finally {
    fixture.app.unmount()
    vi.unstubAllGlobals()
  }
})
