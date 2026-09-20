export interface DiscoveredModel {
  id: string
  name: string
  compatibility: 'recommended' | 'available' | 'unsuitable'
  capabilities?: string[]
}

export interface ModelSelectorOption {
  key: string
  value: string
  label: string
  model?: DiscoveredModel
  manual: boolean
  disabled: boolean
}

export function filterDiscoveredModels(
  models: DiscoveredModel[],
  query: string,
): DiscoveredModel[] {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return models
  return models.filter(model =>
    model.id.toLocaleLowerCase().includes(needle)
      || model.name.toLocaleLowerCase().includes(needle),
  )
}

export function modelSelectorOptions(
  models: DiscoveredModel[],
  query: string,
): ModelSelectorOption[] {
  const candidate = query.trim()
  const discovered = filterDiscoveredModels(models, query).map(model => ({
    key: model.id,
    value: model.id,
    label: model.name,
    model,
    manual: false,
    disabled: model.compatibility === 'unsuitable',
  }))
  const exactMatch = models.some(model => model.id.toLocaleLowerCase() === candidate.toLocaleLowerCase())
  if (!candidate || exactMatch) return discovered
  return [...discovered, {
    key: `manual:${candidate}`,
    value: candidate,
    label: candidate,
    manual: true,
    disabled: false,
  }]
}

// Group headings for the option list.
//
// A model endpoint serves far more than the handful anyone should pick from.
// Discovery already removes what cannot answer a chat request at all; what is
// left still mixes the curated families this platform knows (pricing, context
// window, capability chips) with whatever else the gateway happens to carry.
// The list says which is which rather than leaving a person to recognize an id.
export const RECOMMENDED_GROUP_LABEL = 'Recommended'
export const OTHER_GROUP_LABEL = 'Other models this endpoint serves'
export const MANUAL_GROUP_LABEL = 'Enter manually'

// modelSelectorGroupLabels returns, per option, the heading to render ABOVE it
// or null. It is a parallel array rather than a nested structure on purpose:
// the listbox's active-option arithmetic, its aria-activedescendant ids and
// its keyboard traversal are all indexed on the flat option list, and nesting
// would mean reproducing all three.
//
// Headings appear only when they separate something: a list that is entirely
// recommended, or entirely not, gets none.
export function modelSelectorGroupLabels(options: ModelSelectorOption[]): (string | null)[] {
  const discovered = options.filter(option => !option.manual)
  const recommended = discovered.filter(option => option.model?.compatibility === 'recommended')
  const split = recommended.length > 0 && recommended.length < discovered.length
  let seenRecommended = false
  let seenOther = false
  let seenManual = false
  return options.map(option => {
    if (option.manual) {
      if (seenManual || discovered.length === 0) return null
      seenManual = true
      return MANUAL_GROUP_LABEL
    }
    if (!split) return null
    if (option.model?.compatibility === 'recommended') {
      if (seenRecommended) return null
      seenRecommended = true
      return RECOMMENDED_GROUP_LABEL
    }
    if (seenOther) return null
    seenOther = true
    return OTHER_GROUP_LABEL
  })
}
