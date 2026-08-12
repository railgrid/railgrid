export type PublishingAccessSelection = 'public' | 'members'

export type PublishingStatusTone = 'success' | 'warning' | 'danger' | 'muted'

export interface ProductionBindingState {
  phase?: string | null
  url?: string | null
}

export interface PublishingAccessState {
  published: boolean
  publication?: {
    mode?: string | null
    url?: string | null
    ready?: boolean
  } | null
}

export interface ProductionDeploymentState {
  deployed: boolean
  ready: boolean
  label: string
  tone: PublishingStatusTone
}

export interface ProductionAccessState {
  label: 'Live' | 'Publishing' | 'Offline'
  tone: PublishingStatusTone
  url: string
}

export interface PublishingAccessPresentation {
  label: 'Checking' | 'Disabled' | 'Enabling' | 'Enabled'
  tone: PublishingStatusTone
  description: string
  loading: boolean
}

function normalizedPhase(phase: string | null | undefined): string {
  return typeof phase === 'string' ? phase.trim() : ''
}

function isReadyPhase(phase: string): boolean {
  return phase.toLowerCase() === 'ready'
}

function isFailedPhase(phase: string): boolean {
  switch (phase.toLowerCase()) {
    case 'failed':
    case 'error':
    case 'terminating':
      return true
    default:
      return false
  }
}

// Production deployment and external access are separate lifecycles. A
// missing binding means production has never been promoted; it must not be
// presented as a deployed app merely because an access record is present.
export function productionDeploymentState(binding: ProductionBindingState | null): ProductionDeploymentState {
  if (!binding) {
    return { deployed: false, ready: false, label: 'Offline', tone: 'muted' }
  }
  const phase = normalizedPhase(binding.phase)
  const ready = isReadyPhase(phase)
  return {
    deployed: true,
    ready,
    label: phase || 'Provisioning',
    tone: ready ? 'success' : isFailedPhase(phase) ? 'danger' : 'warning',
  }
}

// The publication view is derived live from the production instance (its
// access value + status URL). A Ready deployment with no publication view is
// truthfully Offline rather than "ready to publish".
export function productionAccessState(
  binding: ProductionBindingState | null,
  publishing: PublishingAccessState | null,
): ProductionAccessState {
  const deployment = productionDeploymentState(binding)
  if (!deployment.ready || !publishing?.published) {
    return { label: 'Offline', tone: 'muted', url: '' }
  }
  const url = publishedApplicationURL(publishing)
  if (publishing.publication?.ready && url) {
    return { label: 'Live', tone: 'success', url }
  }
  return { label: 'Publishing', tone: 'warning', url }
}

export function productionDeploymentDescription(
  binding: ProductionBindingState | null,
  publishing: PublishingAccessState | null,
): string {
  const deployment = productionDeploymentState(binding)
  if (!deployment.ready) {
    return 'The production deployment is being prepared and is not serving external traffic yet.'
  }
  if (publishing === null) {
    return 'The production deployment is running. Checking external access…'
  }
  switch (productionAccessState(binding, publishing).label) {
    case 'Live':
      return 'The production deployment is running and externally accessible.'
    case 'Publishing':
      return 'The production deployment is running while external access is being enabled.'
    default:
      return 'Production is running but not published. Choose Public or Invite-only below, then select Enable access.'
  }
}

export function publishingAccessPresentation(
  state: PublishingAccessState | null,
): PublishingAccessPresentation {
  if (state === null) {
    return {
      label: 'Checking',
      tone: 'muted',
      description: 'Checking the current external access policy…',
      loading: true,
    }
  }
  if (!state.published) {
    return {
      label: 'Disabled',
      tone: 'muted',
      description: 'Choose Public or Invite-only, then select Enable access.',
      loading: false,
    }
  }
  if (state.publication?.ready && publishedApplicationURL(state)) {
    return {
      label: 'Enabled',
      tone: 'success',
      description: 'External access is enabled for this production deployment.',
      loading: false,
    }
  }
  return {
    label: 'Enabling',
    tone: 'warning',
    description: 'External access is being enabled for this production deployment.',
    loading: false,
  }
}

// A missing publication is intentionally invite-only by default. This keeps
// an earlier public project's selection from leaking into the next publish
// action when the user switches projects or unpublishes.
export function publishingAccessSelection(state: PublishingAccessState): PublishingAccessSelection {
  if (state.published && state.publication?.mode === 'public') return 'public'
  return 'members'
}

// The instance's platform-published URL (status.url through the access gate)
// is the only production URL App Studio should expose. The production
// provider binding describes deployment readiness; it is not a public
// routing or access-policy contract.
export function publishedApplicationURL(state: PublishingAccessState | null): string {
  if (!state?.published) return ''
  return state.publication?.url?.trim() || ''
}

export function shouldPollPublishing(state: PublishingAccessState | null): boolean {
  return !!state?.published && (!state.publication?.ready || !publishedApplicationURL(state))
}
