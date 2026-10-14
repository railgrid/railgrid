import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useAuthStore } from './auth'
import { useTenantStore } from './tenant'

// A personal browser preference, never workspace configuration or authority.
export function overlayPreferenceKey(user: string, org: string, workspace: string): string {
  return `railgrid:agent-overlay:${JSON.stringify([user, org, workspace])}`
}

export const useAgentOverlayStore = defineStore('agent-overlay', () => {
  const auth = useAuthStore()
  const tenant = useTenantStore()
  const selectedAgent = ref<string | null>(null)
  const open = ref(false)
  const minimized = ref(false)
  const dockVisible = ref(false)
  const settingsOpen = ref(false)
  const followContext = ref(true)
  const preferenceKey = computed(() => {
    const user = auth.user?.userId || auth.user?.email
    return user && tenant.orgUUID && tenant.workspaceUUID
      ? overlayPreferenceKey(user, tenant.orgUUID, tenant.workspaceUUID) : null
  })
  watch(preferenceKey, key => {
    selectedAgent.value = null
    open.value = false
    settingsOpen.value = false
    dockVisible.value = false
    followContext.value = true
    minimized.value = false
    if (!key) return
    try {
      const saved = JSON.parse(localStorage.getItem(key) || 'null')
      if (typeof saved?.agent === 'string' && saved.agent.length <= 253) selectedAgent.value = saved.agent
      followContext.value = saved?.followContext !== false
      minimized.value = saved?.minimized === true
      open.value = !!selectedAgent.value && !minimized.value
    } catch { /* Corrupt or unavailable storage leaves a usable session preference. */ }
  }, { immediate: true, flush: 'sync' })
  function save(): void {
    if (!preferenceKey.value) return
    try { localStorage.setItem(preferenceKey.value, JSON.stringify({ agent: selectedAgent.value, followContext: followContext.value, minimized: minimized.value })) } catch { /* Session-only preference. */ }
  }
  function select(name: string | null): void {
    if (!preferenceKey.value || (name !== null && (!name || name.length > 253))) return
    selectedAgent.value = name
    minimized.value = false
    open.value = !!name
    save()
  }
  function setMinimized(value: boolean): void { minimized.value = value; open.value = !value && !!selectedAgent.value; save() }
  // Navigation hides the transient surface without changing the user's saved
  // minimized preference. Returning/reloading can still restore the assistant.
  function closeForNavigation(options: { settings?: boolean } = {}): void {
    open.value = false
    dockVisible.value = false
    settingsOpen.value = options.settings === true
  }
  function setSettingsOpen(value: boolean): void { settingsOpen.value = value }
  function setDockVisible(value: boolean): void { dockVisible.value = value }
  function setFollowContext(value: boolean): void { followContext.value = value; save() }
  return { selectedAgent, open, minimized, dockVisible, setMinimized, closeForNavigation, setSettingsOpen, setDockVisible, settingsOpen, followContext, preferenceKey, select, setFollowContext }
})
