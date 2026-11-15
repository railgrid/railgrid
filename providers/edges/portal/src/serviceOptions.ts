import type { CatalogEntry } from './api'
import type { FormSelectOption } from './portalkit/FormSelect.vue'

export const serviceSchemeOptions = [
  { value: 'http', label: 'http' },
  { value: 'https', label: 'https' },
]

export function serviceTypeOptions(catalog: readonly CatalogEntry[]): FormSelectOption[] {
  const groups = new Map<string, FormSelectOption[]>()
  for (const entry of catalog) {
    const group = entry.category || 'Other'
    const option = { value: entry.type, label: entry.displayName, group }
    const options = groups.get(group)
    if (options) options.push(option)
    else groups.set(group, [option])
  }
  return [...groups.values()].flat()
}
