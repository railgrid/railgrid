/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/** A host-shell navigation entry, including optional provider metadata. */
export interface NavItem {
  label: string
  to: string
  // Either a lucide component (static) or a URL string (dynamic provider icon).
  icon?: unknown
  iconURL?: string | null
  key?: string
  exact?: boolean
  /** Stable provider family key used to keep only the active family inline. */
  familyKey?: string
  /** Parent route marks child entries in compact browse menus. */
  parentTo?: string
  /** Short label for an item when its flattened label includes its parent. */
  familyLabel?: string
  children?: Array<{ label: string; to: string }>
}

/** The provider-store shape consumed by the host shell. */
export interface ProviderNavEntry {
  name?: string
  label: string
  to: string
  iconURL?: string | null
  children?: Array<{ label: string; to: string }>
}

/** Grouped provider entries used by both flat shell modes. */
export interface HorizontalSection {
  key: string
  label: string | null
  icon: unknown | null
  items: NavItem[]
}

/** A route-bearing item used by the vertical provider tree. */
export interface ProviderRouteItem {
  to: string
  children?: Array<{ to: string }>
}

/**
 * Flatten provider children for horizontal and floating docks.
 * Child labels stay unambiguous when different providers expose the same
 * page name, and parents become exact matches whenever they have children.
 */
export function flattenProviderItems(items: ProviderNavEntry[]): NavItem[] {
  return items.flatMap((item) => [
    {
      label: item.label,
      to: item.to,
      iconURL: item.iconURL,
      key: item.to,
      exact: Boolean(item.children?.length),
      familyKey: item.to,
      familyLabel: item.label,
    },
    ...(item.children ?? []).map((child) => ({
      label: `${item.label} / ${child.label}`,
      to: child.to,
      iconURL: item.iconURL,
      key: `${item.to}::${child.to}`,
      familyKey: item.to,
      parentTo: item.to,
      familyLabel: child.label,
    })),
  ])
}

/**
 * Return the parent and child routes for the provider family containing the
 * current route. A flat section remains the source of truth, but the family
 * key keeps compact docks from turning the active provider into a memory test.
 */
export function providerFamilyItems(routePath: string, items: NavItem[]): NavItem[] {
  const activeItem = items.find((item) => isActiveRoute(routePath, item.to, item.exact))
  if (!activeItem?.familyKey) return []
  return items.filter((item) => item.familyKey === activeItem.familyKey)
}

/**
 * Match a route to a shell target. Root and the provider catalog are always
 * exact; callers can opt into exact matching for any other parent target.
 */
export function isActiveRoute(routePath: string, targetPath: string, exact = false): boolean {
  if (targetPath === '/' || targetPath === '/providers' || exact) return routePath === targetPath
  return routePath === targetPath || routePath.startsWith(targetPath + '/')
}

/** A provider parent is exact when it owns child routes. */
export function isProviderItemActive(routePath: string, item: ProviderRouteItem): boolean {
  return isActiveRoute(routePath, item.to, Boolean(item.children?.length))
}

/** Keep a provider category open when either its parent or child is active. */
export function hasActiveNavRoute(routePath: string, items: ProviderRouteItem[]): boolean {
  return items.some((item) =>
    isProviderItemActive(routePath, item)
    || (item.children ?? []).some((child) => isActiveRoute(routePath, child.to)),
  )
}
