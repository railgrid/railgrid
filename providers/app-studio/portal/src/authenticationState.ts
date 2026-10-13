export interface AuthenticationEnvironmentState {
  environment: string
  runtimeExists?: boolean
  configured?: boolean
  runtimeReady?: boolean
}

export interface AuthenticationSupportState {
  enabled: boolean
  supported: boolean
  supportReason?: string
  supportMessage?: string
}

export type AuthenticationSupportKind = 'supported' | 'pending' | 'template-unselected' | 'template-missing' | 'unavailable' | 'unknown'
export type AuthenticationStatusTone = 'success' | 'warning' | 'danger' | 'muted'

const unavailableAuthenticationReasons = new Set(['not_declared', 'invalid_contract', 'unknown_version'])

export function authenticationSupportKind(state: AuthenticationSupportState | null | undefined): AuthenticationSupportKind {
  if (!state) return 'unknown'
  if (state.supported) return 'supported'
  switch (state.supportReason) {
    case 'pending': return 'pending'
    case 'template_not_selected': return 'template-unselected'
    case 'template_missing': return 'template-missing'
    default: return unavailableAuthenticationReasons.has(state.supportReason || '') ? 'unavailable' : 'unknown'
  }
}

export function canCreateCompatibleAuthenticationProject(state: AuthenticationSupportState | null | undefined): boolean {
  const kind = authenticationSupportKind(state)
  return !state?.enabled && (kind === 'unavailable' || kind === 'template-missing')
}

export function authenticationSupportStatus(state: AuthenticationSupportState | null | undefined): string {
  switch (authenticationSupportKind(state)) {
    case 'supported': return 'Not enabled'
    case 'pending': return 'Checking support'
    case 'template-unselected': return 'Choose a template'
    case 'template-missing': return 'Template unavailable'
    case 'unavailable':
      return state?.supportReason === 'invalid_contract' ? 'Template needs attention'
        : state?.supportReason === 'unknown_version' ? 'Unsupported version'
          : 'Unavailable for this template'
    default: return 'Support status unavailable'
  }
}

export function authenticationSupportTone(state: AuthenticationSupportState | null | undefined): AuthenticationStatusTone {
  switch (authenticationSupportKind(state)) {
    case 'supported': return 'muted'
    case 'pending':
    case 'unknown': return 'warning'
    case 'unavailable':
      return state?.supportReason === 'invalid_contract' || state?.supportReason === 'unknown_version' ? 'danger' : 'muted'
    case 'template-unselected':
    case 'template-missing': return 'warning'
  }
}

export function authenticationSupportNotice(state: AuthenticationSupportState | null | undefined): string {
  if (!state || state.supported) return ''

  if (state.enabled) {
    switch (authenticationSupportKind(state)) {
      case 'pending': return 'Railgrid Auth remains enabled. The template support check is still running; this check did not change your setting.'
      case 'template-unselected': return 'Railgrid Auth remains enabled, but no development template is selected. Choose a compatible template before changing this setting.'
      case 'template-missing': return 'Railgrid Auth remains enabled, but the selected development template is unavailable. Restore or replace it before disabling Auth.'
      case 'unavailable': return 'Railgrid Auth remains enabled, but this template no longer has verified support. Ask its owner to restore support before disabling it.'
      default: return 'Railgrid Auth remains enabled. Template support could not be confirmed, and this check did not change your setting.'
    }
  }

  let message: string
  switch (authenticationSupportKind(state)) {
    case 'pending':
      message = 'Railgrid Auth support is still being checked. Check again after the development template is Ready.'
      break
    case 'template-unselected':
      message = 'Choose a development template that supports Railgrid Auth in Development settings, then check again.'
      break
    case 'template-missing':
      message = 'The selected development template is no longer available. Create a new project with a compatible template.'
      break
    case 'unavailable':
      if (state.supportReason === 'invalid_contract') {
        message = 'This template cannot use Railgrid Auth right now. Ask its owner to fix the template or create a project with one that supports it.'
      } else if (state.supportReason === 'unknown_version') {
        message = 'This template’s sign-in support is not compatible with this App Studio version. Create a new project with a compatible template.'
      } else {
        message = 'This development template does not support Railgrid Auth. Create a new project with a compatible template.'
      }
      break
    default:
      message = 'We could not confirm whether this template supports Railgrid Auth. Check its status before enabling it.'
  }

  return message
}

export function authenticationSupportIsError(state: AuthenticationSupportState | null | undefined): boolean {
  return Boolean(state && !state.supported && ['template_missing', 'invalid_contract', 'unknown_version'].includes(state.supportReason || ''))
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
