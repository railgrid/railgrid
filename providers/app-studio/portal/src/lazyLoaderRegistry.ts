export type LazySurface = 'page' | 'tile'
export type LazyLoader<T> = () => Promise<T>
export type LazyLoaderRegistry<T> = Record<LazySurface, LazyLoader<T>>

export const APP_STUDIO_LOADER_REGISTRY_KEY = '__farosProviderAppStudioLazyLoadersV1'
export const PROVIDER_BOOTSTRAP_GENERATIONS_KEY = '__farosProviderBootstrapGenerationsV1'

export function isCurrentAppStudioBootstrap(root: object, generation: string | undefined): boolean {
  const registryRoot = root as Record<string, unknown>
  const generations = registryRoot[PROVIDER_BOOTSTRAP_GENERATIONS_KEY] as
    | Record<string, string>
    | undefined
  const current = generations?.['app-studio']
  // Preserve direct/legacy embedding when no host generation registry exists.
  // Once the host establishes the contract, unidentified and stale bodies may
  // not mutate the retained wrapper's loader registry.
  return current === undefined || (generation !== undefined && current === generation)
}

export function installAppStudioLazyLoaders<T>(root: object, loaders: LazyLoaderRegistry<T>): void {
  const registryRoot = root as Record<string, unknown>
  registryRoot[APP_STUDIO_LOADER_REGISTRY_KEY] = loaders
}

export function installCurrentAppStudioLazyLoaders<T>(
  root: object,
  generation: string | undefined,
  loaders: LazyLoaderRegistry<T>,
): boolean {
  if (!isCurrentAppStudioBootstrap(root, generation)) return false
  installAppStudioLazyLoaders(root, loaders)
  return true
}

export function loadCurrentAppStudioSurface<T>(root: object, surface: LazySurface): Promise<T> {
  const registry = (root as Record<string, unknown>)[APP_STUDIO_LOADER_REGISTRY_KEY] as
    | LazyLoaderRegistry<T>
    | undefined
  const loader = registry?.[surface]
  if (!loader) return Promise.reject(new Error(`App Studio ${surface} loader is unavailable`))
  return loader()
}
