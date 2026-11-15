import { expect, it, vi } from 'vitest'

it('mounts the real sharing dialog and exercises preview access and refresh recovery', async () => {
  vi.resetModules()
  document.body.innerHTML = '<div id="app"></div><div id="app-studio-overlay-root"></div>'
  const { app } = await import('./app-studio-sharing.js')
  const button = (name: string) => [...document.querySelectorAll('button')].find(el => el.textContent?.trim() === name)!
  try {
    button('Share project').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Production app'))
    button('Manage preview access').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Who can open this Preview?'))
    expect(document.activeElement?.id).toBe('project-share-preview-title')
    button('Cancel').click()
    await vi.waitFor(() => expect(document.activeElement).toBe(button('Manage preview access')))
    button('Manage production access').click()
    await vi.waitFor(() => expect(document.activeElement?.id).toBe('project-share-production-title'))
    button('Cancel').click()
    await vi.waitFor(() => expect(document.activeElement).toBe(button('Manage production access')))
    button('Manage preview access').click()
    await vi.waitFor(() => expect(document.activeElement?.id).toBe('project-share-preview-title'))
    const publicOption = [...document.querySelectorAll('button')].find(el => el.textContent?.includes('Anyone with the link'))!
    publicOption.click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Anyone with the link could see test data'))
    button('Manage production access').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Discard unsaved Preview access changes?'))
    button('Discard changes').click()
    await vi.waitFor(() => expect(document.activeElement?.id).toBe('project-share-production-title'))
    button('Manage preview access').click()
    await vi.waitFor(() => expect(document.activeElement?.id).toBe('project-share-preview-title'))
    ;[...document.querySelectorAll('button')].find(el => el.textContent?.includes('Anyone with the link'))!.click()
    await vi.waitFor(() => expect(button('Save preview access').disabled).toBe(false))
    button('Save preview access').click()
    await vi.waitFor(() => expect(button('Save preview access').disabled).toBe(true))
    button('Done').click()
    const scenario = document.querySelector('select')!
    scenario.value = 'partial'
    scenario.dispatchEvent(new Event('change', { bubbles: true }))
    button('Share project').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Could not refresh workspace members.'))
    button('Retry').click()
    await vi.waitFor(() => expect(document.body.textContent).not.toContain('Could not refresh workspace members.'))
    button('Manage production access').click()
    await vi.waitFor(() => expect(document.activeElement?.id).toBe('project-share-production-title'))
    button('Disable access').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('The production deployment will keep running.'))
    document.querySelector<HTMLButtonElement>('[role="alertdialog"] .k-modal-btn--confirm')!.click()
    await vi.waitFor(() => expect(document.body.textContent).not.toContain('Disable external access?'))
    // A viewer refresh failure must not silently republish a disabled app.
    scenario.value = 'partial'
    scenario.dispatchEvent(new Event('change', { bubbles: true }))
    button('Share project').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Deployable'))
    expect(document.body.textContent).toContain('Could not refresh workspace members.')
    button('Retry').click()
    await vi.waitFor(() => expect(document.body.textContent).not.toContain('Could not refresh workspace members.'))
    await vi.waitFor(() => expect(button('Enable production access')).toBeTruthy())
    button('Enable production access').click()
    await vi.waitFor(() => expect(button('Save production access')).toBeTruthy())
    expect(button('Save production access').disabled).toBe(true)
    button('Done').click()
    scenario.value = 'undeployed'
    scenario.dispatchEvent(new Event('change', { bubbles: true }))
    button('Share project').click()
    await vi.waitFor(() => expect(document.body.textContent).toContain('Deploy Production before choosing its audience'))
  } finally {
    app.unmount()
  }
})
