import { shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { normalizePageResource, type PageResourceHint } from '@/portalkit/page-context'

export const usePageResourceStore = defineStore('page-resource', () => {
  const snapshot = shallowRef<{ scope: string; resource: PageResourceHint } | null>(null)
  let owner: object | null = null
  function publish(source: object, scope: string, value: unknown): void {
    const resource = normalizePageResource(value)
    owner = source
    snapshot.value = resource ? { scope, resource } : null
  }
  function clear(source: object): void {
    if (owner !== source) return
    owner = null
    snapshot.value = null
  }
  return { snapshot, publish, clear }
})
