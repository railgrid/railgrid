import { computed, ref, watch } from 'vue'
import { toast } from '@/portalkit/toast'
import type {
  AcceptedClaim,
  AcceptedComposition,
  AcceptedHubAccess,
  ProviderDTO,
} from '@/stores/providers'

interface ProviderEnableFlowOptions {
  scope: () => string
  enable: (
    provider: ProviderDTO,
    accept: AcceptedClaim[],
    acceptHubAccess: AcceptedHubAccess[],
    acceptCompositions: AcceptedComposition[],
  ) => Promise<unknown>
  dependencyError?: (provider: ProviderDTO) => string | null
}

/** Shares the consent dialog's workspace fencing and operation ownership. */
export function useProviderEnableFlow(options: ProviderEnableFlowOptions) {
  const busy = ref<Record<string, boolean>>({})
  const actionError = ref<string | null>(null)
  const dialogProvider = ref<ProviderDTO | null>(null)
  const dialogRevision = ref(0)
  const dialogScope = computed(options.scope)
  const enableOperations = new Map<string, symbol>()

  function openEnableDialog(provider: ProviderDTO) {
    if (busy.value[provider.name]) return
    actionError.value = null
    const error = options.dependencyError?.(provider)
    if (error) {
      actionError.value = error
      return
    }
    dialogRevision.value += 1
    dialogProvider.value = provider
  }

  function closeEnableDialog() {
    dialogRevision.value += 1
    dialogProvider.value = null
    actionError.value = null
  }

  watch(dialogScope, () => {
    // A context switch invalidates the visible consent decision. Clear owner
    // tokens as well so an older request cannot release a newer operation.
    if (dialogProvider.value) closeEnableDialog()
    enableOperations.clear()
    busy.value = {}
  })

  async function onDialogConfirm(
    accept: AcceptedClaim[],
    acceptHubAccess: AcceptedHubAccess[] = [],
    acceptCompositions: AcceptedComposition[] = [],
  ) {
    const provider = dialogProvider.value
    const revision = dialogRevision.value
    const scope = dialogScope.value
    if (!provider || busy.value[provider.name]) return

    const operation = Symbol(provider.name)
    enableOperations.set(provider.name, operation)
    busy.value = { ...busy.value, [provider.name]: true }
    actionError.value = null
    try {
      await options.enable(provider, accept, acceptHubAccess, acceptCompositions)
      if (dialogRevision.value === revision && dialogProvider.value === provider && dialogScope.value === scope) {
        closeEnableDialog()
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (dialogRevision.value === revision && dialogProvider.value === provider && dialogScope.value === scope) {
        actionError.value = message
      } else if (dialogScope.value === scope) {
        toast('error', `Could not enable ${provider.displayName}: ${message}`, {
          scope,
          source: 'provider-enable',
          dedupeKey: `provider-enable:${provider.name}`,
        })
      }
    } finally {
      if (enableOperations.get(provider.name) === operation) {
        enableOperations.delete(provider.name)
        const next = { ...busy.value }
        delete next[provider.name]
        busy.value = next
      }
    }
  }

  return {
    busy,
    actionError,
    dialogProvider,
    dialogRevision,
    dialogScope,
    openEnableDialog,
    closeEnableDialog,
    onDialogConfirm,
  }
}
