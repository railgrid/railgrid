export interface OnboardingPlanItem<T> {
  name: string
  provider?: T
}

export function planFor<T extends { requires?: Array<{ provider?: string }> }>(
  goal: string,
  byName: (name: string) => T | undefined | null,
): OnboardingPlanItem<T>[] {
  const rows: OnboardingPlanItem<T>[] = []
  const seen = new Set<string>()
  function visit(name: string) {
    if (seen.has(name)) return
    seen.add(name)
    const provider = byName(name) ?? undefined
    for (const requirement of provider?.requires ?? []) {
      if (requirement.provider) visit(requirement.provider)
    }
    rows.push({ name, provider })
  }
  if (goal !== 'explore') visit(goal)
  return rows
}

export function setupReadyFor<T extends { ready?: boolean }>(
  dataCurrent: boolean,
  canOpen: boolean,
  plan: OnboardingPlanItem<T>[],
  isEnabled: (name: string) => boolean,
): boolean {
  return dataCurrent && canOpen && plan.every(item => !!item.provider?.ready && isEnabled(item.name))
}
