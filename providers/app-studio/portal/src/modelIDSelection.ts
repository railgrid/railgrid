import type { ProjectLLMDiscoveredModel } from './types'

export interface ModelSelectorOption {
  key: string
  value: string
  label: string
  model?: ProjectLLMDiscoveredModel
  manual: boolean
  disabled: boolean
}

export function filterDiscoveredModels(
  models: ProjectLLMDiscoveredModel[],
  query: string,
): ProjectLLMDiscoveredModel[] {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return models
  return models.filter(model =>
    model.id.toLocaleLowerCase().includes(needle)
      || model.name.toLocaleLowerCase().includes(needle),
  )
}

export function modelSelectorOptions(
  models: ProjectLLMDiscoveredModel[],
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
