export interface ProviderLoadGeneration {
  begin: () => number
  isCurrent: (generation: number) => boolean
  invalidate: () => void
}

/**
 * Small latest-wins fence for async provider consumers. Script loads are
 * shared across the shell, but each mount point must independently reject a
 * completion that belongs to stale catalog props or an unmounted component.
 */
export function createProviderLoadGeneration(): ProviderLoadGeneration {
  let current = 0
  return {
    begin: () => ++current,
    isCurrent: (generation) => generation === current,
    invalidate: () => { current++ },
  }
}
