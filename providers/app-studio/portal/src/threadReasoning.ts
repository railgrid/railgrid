export type ReasoningEffortByModel = Record<string, string>

export function threadReasoningEffort(
  preferences: ReasoningEffortByModel | undefined,
  modelID: string,
): string {
  if (!modelID) return ''
  const value = preferences?.[modelID]
  return typeof value === 'string' ? value : ''
}

export function updateThreadReasoningEffort(
  preferences: ReasoningEffortByModel | undefined,
  modelID: string,
  effort: string,
): ReasoningEffortByModel {
  if (!modelID) return { ...(preferences ?? {}) }
  const next = { ...(preferences ?? {}) }
  if (effort === '') delete next[modelID]
  else next[modelID] = effort
  return next
}

export function initialReasoningEffortByModel(modelID: string, effort: string): ReasoningEffortByModel | undefined {
  return modelID && effort ? { [modelID]: effort } : undefined
}

export function reasoningEffortsForDraftScope(
  drafts: Record<string, string>,
  scope: readonly string[],
): ReasoningEffortByModel {
  const result: ReasoningEffortByModel = {}
  for (const [key, effort] of Object.entries(drafts)) {
    if (typeof effort !== 'string' || !effort) continue
    let parts: unknown
    try {
      parts = JSON.parse(key)
    } catch {
      continue
    }
    if (!Array.isArray(parts) || parts.length !== scope.length + 1) continue
    if (!scope.every((value, index) => parts[index] === value)) continue
    const modelID = parts[scope.length]
    if (typeof modelID !== 'string' || !modelID) continue
    result[modelID] = effort
  }
  return result
}

export function initialReasoningEffortSnapshot(
  preferences: ReasoningEffortByModel | undefined,
  modelID: string,
  effort: string,
): ReasoningEffortByModel | undefined {
  const result = { ...(preferences ?? {}) }
  if (modelID) {
    if (effort) result[modelID] = effort
    else delete result[modelID]
  }
  return Object.keys(result).length ? result : undefined
}

export function supportedReasoningEffortSnapshot(
  preferences: ReasoningEffortByModel | undefined,
  supportedEffortsByModel: Record<string, readonly string[]>,
): ReasoningEffortByModel | undefined {
  const supportedPreferences = Object.entries(preferences ?? {}).filter(([modelID, effort]) => {
    const supportedEfforts = Object.prototype.hasOwnProperty.call(supportedEffortsByModel, modelID)
      ? supportedEffortsByModel[modelID]
      : undefined
    return supportedEfforts?.includes(effort) ?? false
  })
  return supportedPreferences.length ? Object.fromEntries(supportedPreferences) : undefined
}

export function overlayThreadReasoningPreferenceDrafts(
  canonical: ReasoningEffortByModel | undefined,
  drafts: Record<string, string>,
  contextFingerprint: string,
  projectName: string,
  threadID: string,
): ReasoningEffortByModel {
  const result = { ...(canonical ?? {}) }
  for (const [key, effort] of Object.entries(drafts)) {
    let scope: unknown
    try {
      scope = JSON.parse(key)
    } catch {
      continue
    }
    if (!Array.isArray(scope) || scope.length !== 4 || scope[0] !== contextFingerprint || scope[1] !== projectName || scope[2] !== threadID || typeof scope[3] !== 'string' || !scope[3]) continue
    if (effort) result[scope[3]] = effort
    else delete result[scope[3]]
  }
  return result
}
