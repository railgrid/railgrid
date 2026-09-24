import { nextTick, ref, watch } from 'vue'
import type { TableSelectionKey } from '@/portalkit/table'

export interface SettingsBulkItem {
  key: string
  name: string
}

export interface SettingsBulkOutcome {
  key: string
  name: string
  succeeded: boolean
  error?: string
}

export interface SettingsBulkActionOptions<TItem extends SettingsBulkItem, TContext> {
  captureContext: () => TContext | null
  isContextCurrent: (context: TContext) => boolean
  resolveItems: (context: TContext, keys: string[]) => TItem[]
  /** A stable serialization of the target fields the user confirmed. */
  snapshotItem: (item: TItem) => string
  /** Return null when the item remains safe to mutate. */
  ineligibleReason: (context: TContext, item: TItem) => string | null
  confirm: (context: TContext, items: TItem[]) => Promise<boolean>
  mutate: (context: TContext, item: TItem) => Promise<boolean>
  clearError: () => void
  readError: () => string | null
  onSuccess: (context: TContext, item: TItem) => void
  refresh: (context: TContext) => Promise<void>
  fallbackError: string
}

function normalizedKeys(keys: readonly TableSelectionKey[]): string[] {
  return [...new Set(keys.map((key) => String(key)).filter(Boolean))]
}

function sameKeys(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((key) => right.includes(key))
}

/**
 * Shared confirm / revalidate / sequential mutation flow for destructive
 * settings selections. The page owns scope epochs; this helper owns the
 * selection revision, progress lock, and per-item outcomes.
 */
export function useSettingsBulkAction<TItem extends SettingsBulkItem, TContext>(
  options: SettingsBulkActionOptions<TItem, TContext>,
) {
  const selectedKeys = ref<TableSelectionKey[]>([])
  const busy = ref(false)
  const outcomes = ref<SettingsBulkOutcome[]>([])
  let selectionRevision = 0
  let pendingConfirmation = false

  watch(selectedKeys, () => { selectionRevision++ }, { deep: true, flush: 'sync' })

  function resetSelection(): void {
    selectedKeys.value = []
    outcomes.value = []
  }

  async function run(keys: readonly TableSelectionKey[] = selectedKeys.value): Promise<void> {
    if (busy.value || pendingConfirmation) return
    const requestedKeys = normalizedKeys(keys)
    const selectionAtStart = normalizedKeys(selectedKeys.value)
    if (!requestedKeys.length || !sameKeys(requestedKeys, selectionAtStart)) return

    const context = options.captureContext()
    if (!context || !options.isContextCurrent(context)) return

    const initialItems = options.resolveItems(context, requestedKeys)
    if (initialItems.length !== requestedKeys.length ||
      initialItems.some((item, index) => item.key !== requestedKeys[index]) ||
      initialItems.some((item) => options.ineligibleReason(context, item))) return

    const snapshots = new Map(initialItems.map((item) => [item.key, options.snapshotItem(item)]))
    const revisionAtPrompt = selectionRevision
    pendingConfirmation = true
    try {
      const confirmed = await options.confirm(context, initialItems)
      if (!confirmed) return
      // Let ConfirmDialog restore focus to the still-enabled selection action
      // before the worker lock disables it for the network mutations.
      await nextTick()
      await nextTick()
      if (!options.isContextCurrent(context) ||
        selectionRevision !== revisionAtPrompt ||
        !sameKeys(normalizedKeys(selectedKeys.value), selectionAtStart)) return

      const refreshedItems = options.resolveItems(context, requestedKeys)
      const refreshedByKey = new Map(refreshedItems.map((item) => [item.key, item]))
      const invalidOutcomes: SettingsBulkOutcome[] = []
      for (const original of initialItems) {
        const current = refreshedByKey.get(original.key)
        const reason = !current
          ? 'This item is no longer in the current list. Refresh and review the selection.'
          : snapshots.get(original.key) !== options.snapshotItem(current)
            ? 'This item changed while confirmation was open. Review it and try again.'
            : options.ineligibleReason(context, current)
        if (reason) invalidOutcomes.push({ key: original.key, name: original.name, succeeded: false, error: reason })
      }
      if (!options.isContextCurrent(context)) return
      if (refreshedItems.length !== requestedKeys.length || invalidOutcomes.length) {
        outcomes.value = invalidOutcomes.length
          ? invalidOutcomes
          : initialItems.map((item) => ({
            key: item.key,
            name: item.name,
            succeeded: false,
            error: 'The selection changed while confirmation was open. Review it and try again.',
        }))
        return
      }

      busy.value = true
      const resultByKey = new Map<string, SettingsBulkOutcome>()
      let remainingKeys = [...selectionAtStart]
      let expectedSelectionRevision = selectionRevision
      let succeededAny = false

      for (const confirmedItem of refreshedItems) {
        if (!options.isContextCurrent(context)) break
        if (selectionRevision !== expectedSelectionRevision ||
          !sameKeys(normalizedKeys(selectedKeys.value), remainingKeys)) break

        const current = options.resolveItems(context, [confirmedItem.key])[0]
        const reason = !current
          ? 'This item is no longer in the current list. Refresh and review the selection.'
          : snapshots.get(confirmedItem.key) !== options.snapshotItem(current)
            ? 'This item changed during the operation. Review it and try again.'
            : options.ineligibleReason(context, current)
        if (reason) {
          resultByKey.set(confirmedItem.key, {
            key: confirmedItem.key,
            name: confirmedItem.name,
            succeeded: false,
            error: reason,
          })
          continue
        }

        options.clearError()
        let succeeded = false
        let errorMessage: string | undefined
        try {
          succeeded = await options.mutate(context, current)
          if (!succeeded) errorMessage = options.readError() || options.fallbackError
        } catch (error: unknown) {
          errorMessage = error instanceof Error ? error.message : options.fallbackError
        }

        // A route, workspace, or permission transition retires this run even
        // if the request just completed successfully. Its page state has been
        // cleared by the synchronous scope watcher.
        if (!options.isContextCurrent(context)) break
        const selectionChanged = selectionRevision !== expectedSelectionRevision ||
          !sameKeys(normalizedKeys(selectedKeys.value), remainingKeys)

        if (succeeded) {
          succeededAny = true
          options.onSuccess(context, current)
          resultByKey.set(current.key, { key: current.key, name: current.name, succeeded: true })
          remainingKeys = remainingKeys.filter((key) => key !== current.key)
          selectedKeys.value = selectedKeys.value.filter((key) => String(key) !== current.key)
          expectedSelectionRevision = selectionRevision
        } else {
          resultByKey.set(current.key, {
            key: current.key,
            name: current.name,
            succeeded: false,
            error: errorMessage || options.fallbackError,
          })
        }
        // A UI selection change is normally impossible while busy, but keep
        // the just-finished result truthful if a parent resets selection. Stop
        // the remaining queue without restoring or changing that selection.
        if (selectionChanged) break
      }

      if (!options.isContextCurrent(context)) return
      outcomes.value = initialItems.flatMap((item) => {
        const result = resultByKey.get(item.key)
        return result ? [result] : []
      })
      if (succeededAny) {
        try {
          await options.refresh(context)
        } catch {
          // The individual list loaders retain their last good snapshot and
          // surface read failures in the table's existing stale state.
        }
      }
    } finally {
      pendingConfirmation = false
      busy.value = false
    }
  }

  return {
    selectedKeys,
    busy,
    outcomes,
    resolveItems: options.resolveItems,
    ineligibleReason: options.ineligibleReason,
    resetSelection,
    run,
  }
}
