import { audienceCopy, defaultGoalForAudience, goalCopy } from './onboardingCopy'

export interface OnboardingState {
  step: number
  audience: string
  goal: string
  goalExplicit: boolean
}

export function isConfirmedAdmin(workspaceRole?: string, orgRole?: string): boolean {
  return workspaceRole === 'admin' || orgRole === 'admin'
}

export function onboardingStorageKey(identity: string, scope: string): string {
  return identity ? `railgrid:portal:onboarding:user:${encodeURIComponent(identity)}:${scope}` : ''
}

export function restoreOnboardingState(raw: string | null, startAtGoal = false): OnboardingState {
  const defaultState: OnboardingState = {
    step: startAtGoal ? 1 : 0,
    audience: 'team',
    goal: defaultGoalForAudience('team'),
    goalExplicit: false,
  }
  try {
    const saved: unknown = JSON.parse(raw || 'null')
    if (!saved || typeof saved !== 'object') return defaultState
    const value = saved as Record<string, unknown>
    if (!audienceCopy.some(option => option.id === value.audience)) return defaultState
    const audience = value.audience as string
    const step = value.step === undefined ? 0 : value.step
    if (step !== 0 && step !== 1 && step !== 2) return defaultState

    if (value.goal === 'explore') {
      if (step !== 2) return defaultState
      return {
        step: startAtGoal ? 1 : 2,
        audience,
        goal: startAtGoal ? defaultGoalForAudience(audience) : 'explore',
        goalExplicit: false,
      }
    }

    if (!goalCopy.some(option => option.id === value.goal)) return defaultState
    const goal = value.goal as string
    const goalExplicit = value.goalExplicit === true || (value.goalExplicit === undefined && step > 0)
    return {
      step: startAtGoal ? 1 : step,
      audience,
      goal: goalExplicit ? goal : defaultGoalForAudience(audience),
      goalExplicit,
    }
  } catch {
    return defaultState
  }
}

export function persistOnboardingState(state: OnboardingState): string {
  return JSON.stringify(state)
}
