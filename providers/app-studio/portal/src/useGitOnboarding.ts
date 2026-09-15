import { computed, ref, watch } from 'vue'
import type { RailgridContext } from './types'

export function gitOnboardingStorageKey(ctx: RailgridContext | null): string | null {
  const user = ctx?.user?.sub || ctx?.user?.userId || ctx?.user?.email
  if (!user || !ctx?.orgUUID || !ctx?.workspaceUUID) return null
  return `railgrid:app-studio:git-skipped:${JSON.stringify([user, ctx.orgUUID, ctx.workspaceUUID])}`
}

export function useGitOnboarding(context: () => RailgridContext | null) {
  const skipped = ref(false)
  const key = computed(() => gitOnboardingStorageKey(context()))
  watch(key, (value) => {
    try { skipped.value = value ? localStorage.getItem(value) === '1' : false }
    catch { skipped.value = false }
  }, { immediate: true })
  function setSkipped(value: boolean) {
    skipped.value = value
    try { if (key.value) value ? localStorage.setItem(key.value, '1') : localStorage.removeItem(key.value) } catch { /* Session-only when storage is unavailable. */ }
  }
  return { skipped, skip: () => setSkipped(true), reset: () => setSkipped(false) }
}
