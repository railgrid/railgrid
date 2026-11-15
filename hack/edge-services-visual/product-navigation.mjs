// Mirror ProviderFrame's context and bubbling navigation contract while keeping
// scenario query parameters intact for reloads and browser history.
export function installFixtureNavigation({ element, initialPath, fixtureFetch, getTheme, window }) {
  const applyContext = () => {
    const hash = window.location.hash
    const subPath = hash.startsWith('#/') ? decodeURIComponent(hash.slice(2)) : initialPath
    element.railgridContext = { tenant: 'review-workspace', fetch: fixtureFetch, theme: getTheme(), subPath }
  }
  element.addEventListener('railgrid-navigate', event => {
    const { path, replace } = event.detail
    const url = new URL(window.location.href)
    url.hash = `/${encodeURIComponent(path)}`
    window.history[replace ? 'replaceState' : 'pushState'](null, '', url)
    applyContext()
  })
  window.addEventListener('popstate', applyContext)
  applyContext()
  return applyContext
}
