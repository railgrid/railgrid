import { computed, ref, shallowReactive, shallowRef, watch } from 'vue'
import { defineStore } from 'pinia'
import { authFetch } from '@/auth/session'
import { authSessionRevision } from '@/auth/token'
import { toast } from '@/portalkit/toast'
import { useAuthStore } from './auth'
import { useTenantStore } from './tenant'

// Account choice is cached per browser alongside browser-only presentation state.
interface AssistantPreference {
  agent?: string
  followContext: boolean
}

type PreferenceWrite =
  | { method: 'PATCH'; settings: Partial<AssistantPreference> }
  | { method: 'PUT'; settings: AssistantPreference & { initialize?: boolean; expected?: { preference: AssistantPreference | null } } }

export function overlayPreferenceKey(user: string, org: string, workspace: string): string {
  return `railgrid:agent-overlay:${JSON.stringify([user, org, workspace])}`
}

export const useAgentOverlayStore = defineStore('agent-overlay', () => {
  const auth = useAuthStore()
  const tenant = useTenantStore()
  const personalAgent = ref<string | null | undefined>(undefined)
  const defaultAgent = ref<string | null>(null)
  const selectedAgent = computed(() => personalAgent.value === undefined ? defaultAgent.value : personalAgent.value)
  const dashboardSubmit = shallowRef<((prompt: string, origin: HTMLElement | null) => boolean) | null>(null)
  const dashboardExpanded = ref(false)
  const dashboardPreparing = ref(false)
  const defaultAgentLoading = ref(false)
  const preferencesLoading = ref(false)
  const open = ref(false)
  const minimized = ref(false)
  const chatLauncherVisible = ref(false)
  const sidePanelWidth = ref(0)
  const sidePanelFullscreen = ref(false)
  const sidePanelClosing = ref(false)
  const settingsOpen = ref(false)
  const followContext = ref(true)
  // Runtime only: the mounted assistant reports an active conversation. A
  // full page load would drop it, so automatic reloads defer to the user.
  const conversationOpen = ref(false)
  const preferenceKey = computed(() => {
    const user = auth.user?.userId || auth.user?.email
    return user && tenant.orgUUID && tenant.workspaceUUID
      ? overlayPreferenceKey(user, tenant.orgUUID, tenant.workspaceUUID) : null
  })
  let revision = 0
  let confirmedPreference: AssistantPreference | null | undefined
  let cachedContext = true
  const writes = shallowReactive(new Map<string, Promise<void>>())
  const preferencesSaving = computed(() => !!preferenceKey.value && writes.has(preferenceKey.value))

  function preferenceEndpoint() {
    const org = tenant.orgUUID
    const workspace = tenant.workspaceUUID
    if (!auth.token || !org || !workspace) return
    return {
      path: `/api/orgs/${encodeURIComponent(org)}/workspaces/${encodeURIComponent(workspace)}/assistant-preference`,
      headers: { 'X-Railgrid-Org': org, 'X-Railgrid-Workspace': workspace },
    }
  }

  type Endpoint = NonNullable<ReturnType<typeof preferenceEndpoint>>

  async function requestPreference(endpoint: Endpoint, signal?: AbortSignal, write?: PreferenceWrite) {
    const response = await authFetch(endpoint.path, write
      ? { method: write.method, signal, headers: { ...endpoint.headers, 'Content-Type': 'application/json' }, body: JSON.stringify(write.settings) }
      : { signal, headers: endpoint.headers },
    )
    const conflict = write?.method === 'PUT' && response.status === 409
    if (!response.ok && !conflict) throw new Error(`Assistant preferences returned HTTP ${response.status}`)
    const { preference } = await response.json() as { preference: AssistantPreference | null }
    if (preference !== null && (!preference || typeof preference.followContext !== 'boolean' ||
      (preference.agent !== undefined && typeof preference.agent !== 'string'))) {
      throw new Error('Invalid assistant preferences')
    }
    return { preference, conflict }
  }

  function cache(): void {
    if (!preferenceKey.value) return
    try {
      localStorage.setItem(preferenceKey.value, JSON.stringify({
        agent: personalAgent.value === null ? '' : personalAgent.value,
        followContext: auth.token ? cachedContext : followContext.value,
        minimized: minimized.value,
      }))
    } catch { /* Storage is optional for the account preference. */ }
  }

  function applyPreference(settings: AssistantPreference | null, restoreOpen = false): void {
    const previousAgent = selectedAgent.value
    confirmedPreference = settings
    cachedContext = settings?.followContext ?? true
    personalAgent.value = settings?.agent === '' ? null : settings?.agent
    followContext.value = settings?.followContext ?? true
    if (restoreOpen || selectedAgent.value !== previousAgent) open.value = !!selectedAgent.value && !minimized.value
  }

  watch([preferenceKey, () => Boolean(auth.token)], ([key], _, onCleanup) => {
    const currentRevision = ++revision
    confirmedPreference = undefined
    cachedContext = true
    personalAgent.value = undefined
    defaultAgent.value = null
    open.value = false
    settingsOpen.value = false
    sidePanelWidth.value = 0
    sidePanelFullscreen.value = false
    sidePanelClosing.value = false
    const endpoint = key ? preferenceEndpoint() : undefined
    followContext.value = !endpoint
    minimized.value = false
    preferencesLoading.value = !!endpoint
    if (!key) return
    let legacyContext = true
    try {
      const saved = JSON.parse(localStorage.getItem(key) || 'null')
      // The old cache also wrote null when there was no agent choice.
      if (typeof saved?.agent === 'string' && saved.agent.length <= 253) personalAgent.value = saved.agent === '' ? null : saved.agent
      legacyContext = saved?.followContext !== false
      cachedContext = legacyContext
      if (!endpoint) followContext.value = legacyContext
      minimized.value = saved?.minimized === true
      open.value = !!selectedAgent.value && !minimized.value
    } catch { /* Corrupt or unavailable storage leaves a usable session preference. */ }
    if (!endpoint) return
    const legacyAgent = personalAgent.value === null ? '' : personalAgent.value

    const controller = new AbortController()
    onCleanup(() => controller.abort())
    const current = () => !controller.signal.aborted && currentRevision === revision

    void (async () => {
      try {
        await writes.get(key)
        if (!current()) return
        let { preference } = await requestPreference(endpoint, controller.signal)
        if (!current()) return
        if (preference === null && (legacyAgent !== undefined || !legacyContext)) {
          ({ preference } = await requestPreference(endpoint, controller.signal, {
            method: 'PUT', settings: { agent: legacyAgent, followContext: legacyContext, initialize: true },
          }))
        }
        if (current()) {
          applyPreference(preference, true)
          cache()
        }
      } catch { /* Keep the cached agent available with page context off. */ }
      finally { if (current()) preferencesLoading.value = false }
    })()
  }, { immediate: true, flush: 'sync' })

  function save(change: PreferenceWrite): void {
    const key = preferenceKey.value
    if (!key) return
    revision++
    const endpoint = preferenceEndpoint()
    if (endpoint) followContext.value = false
    else if (change.settings.followContext !== undefined) followContext.value = change.settings.followContext
    preferencesLoading.value = false
    cache()
    if (!endpoint) return
    const session = authSessionRevision()
    const edit = revision
    const write = (writes.get(key) ?? Promise.resolve()).then(async () => {
      if (authSessionRevision() !== session) return
      try {
        const result = await requestPreference(endpoint, undefined, change)
        if (key === preferenceKey.value && authSessionRevision() === session) {
          confirmedPreference = result.preference
          cachedContext = result.preference?.followContext ?? true
          if (edit !== revision) {
            cache()
            return
          }
          const configured = change.method === 'PUT' && !result.conflict
          if (configured) minimized.value = false
          applyPreference(result.preference, configured)
          cache()
          if (result.conflict) toast('error', 'Assistant settings changed in another tab. Review the current settings and save again.')
        }
      } catch {
        if (key === preferenceKey.value && edit === revision && authSessionRevision() === session) {
          if (confirmedPreference !== undefined) followContext.value = confirmedPreference?.followContext ?? true
          toast('error', 'Couldn’t save the assistant to your account. This change may only apply in this browser.')
        }
      }
    })
    writes.set(key, write)
    void write.finally(() => { if (writes.get(key) === write) writes.delete(key) })
  }

  function chooseAgent(name: string | null): void {
    personalAgent.value = name
    minimized.value = false
    open.value = !!name
  }

  async function configure(name: string | null, includeContext: boolean): Promise<void> {
    const key = preferenceKey.value
    if (!key || (name !== null && (!name || name.length > 253))) return
    const endpoint = preferenceEndpoint()
    if (!endpoint) {
      chooseAgent(name)
      followContext.value = includeContext
      cache()
      return
    }
    if (confirmedPreference === undefined) {
      if (preferencesLoading.value) return
      const session = authSessionRevision()
      const edit = revision
      const current = () => key === preferenceKey.value && edit === revision && authSessionRevision() === session
      preferencesLoading.value = true
      try {
        const { preference } = await requestPreference(endpoint)
        if (!current()) return
        confirmedPreference = preference
        cachedContext = preference?.followContext ?? true
      } catch {
        if (current()) toast('error', 'Couldn’t confirm your account settings. Try saving again.')
        return
      } finally {
        if (current()) preferencesLoading.value = false
      }
    }
    // Keep the old selection until the account accepts the full save.
    save({ method: 'PUT', settings: {
      agent: name ?? '', followContext: includeContext, expected: { preference: confirmedPreference },
    } })
  }

  function select(name: string | null): void {
    if (!preferenceKey.value || (name !== null && (!name || name.length > 253))) return
    chooseAgent(name)
    save({ method: 'PATCH', settings: { agent: name ?? '' } })
  }
  function setDefaultAgent(name: string | null): void {
    defaultAgent.value = name
    if (personalAgent.value === undefined) open.value = !!name && !minimized.value
  }
  function setMinimized(value: boolean): void { minimized.value = value; open.value = !value && !!selectedAgent.value; cache() }
  // Navigation hides the transient surface without changing the user's saved
  // minimized preference. Returning/reloading can still restore the assistant.
  function closeForNavigation(options: { settings?: boolean } = {}): void {
    open.value = false
    sidePanelWidth.value = 0
    sidePanelFullscreen.value = false
    sidePanelClosing.value = false
    settingsOpen.value = options.settings === true
  }
  function setSidePanel(width: number, fullscreen = false): void {
    sidePanelWidth.value = Number.isFinite(width) ? Math.max(0, width) : 0
    sidePanelFullscreen.value = fullscreen
  }
  function setSidePanelClosing(value: boolean): void { sidePanelClosing.value = value }
  function setSettingsOpen(value: boolean): void { settingsOpen.value = value }
  function setChatLauncherVisible(value: boolean): void { chatLauncherVisible.value = value }
  function setFollowContext(value: boolean): void {
    save({ method: 'PATCH', settings: { followContext: value } })
  }
  function setConversationOpen(value: boolean): void { conversationOpen.value = value }
  return { defaultAgentLoading, preferencesLoading, preferencesSaving, personalAgent, configure, setDefaultAgent, dashboardSubmit, dashboardExpanded, dashboardPreparing, sidePanelClosing, setSidePanelClosing, sidePanelWidth, sidePanelFullscreen, setSidePanel, selectedAgent, open, minimized, chatLauncherVisible, setChatLauncherVisible, setMinimized, closeForNavigation, setSettingsOpen, settingsOpen, followContext, preferenceKey, select, setFollowContext, conversationOpen, setConversationOpen }
})
