export interface AuthenticationEnvironmentState {
  environment: string
  runtimeExists?: boolean
  configured?: boolean
  runtimeReady?: boolean
}

function productionEnvironments(environments: readonly AuthenticationEnvironmentState[] | null | undefined) {
  return (environments || []).filter(environment => environment.environment === 'production')
}

export function hasPersistedProductionRuntime(
  environments: readonly AuthenticationEnvironmentState[] | null | undefined,
): boolean {
  const production = productionEnvironments(environments)
  return production.length === 1 && production[0].runtimeExists === true
}

export function shouldLoadAuthenticationGrants(
  environments: readonly AuthenticationEnvironmentState[] | null | undefined,
): boolean {
  const production = productionEnvironments(environments)
  if (production.length === 0) return false
  if (production.length !== 1) return true
  // Older provider responses do not include runtimeExists. Keep showing any
  // existing grants until the provider can positively confirm there is no target.
  return production[0].runtimeExists !== false
}
