import { ref, watch } from 'vue'
import type { AppStore } from '../store'

interface DeleteFailure {
  title: string
  message: string
}

export function useTableDeletionFeedback(getStore: () => AppStore) {
  const failures = ref<Record<string, DeleteFailure>>({})
  watch(getStore, () => { failures.value = {} }, { flush: 'sync' })

  function fail(key: string, resource: string, name: string, message: string): true {
    failures.value = { ...failures.value, [key]: { title: `Could not delete ${resource} ${name}`, message } }
    return true
  }

  function clear(key: string) {
    const next = { ...failures.value }
    delete next[key]
    failures.value = next
  }

  return { failures, fail, clear }
}

export function sameDeleteTarget<T>(expected: T, current: T | undefined, uidOf: (item: T) => string | undefined): boolean {
  if (!current) return false
  const uid = uidOf(expected)
  return uid ? uid === uidOf(current) : expected === current
}
