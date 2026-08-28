export interface FarosNavigationDetail {
  path: string
  replace?: true
}

/** Build the detail carried by the provider-to-shell navigation event. */
export function navigationDetail(path: string, replace = false): FarosNavigationDetail {
  const relativePath = path.replace(/^\/+/, '')
  return replace ? { path: relativePath, replace: true } : { path: relativePath }
}
