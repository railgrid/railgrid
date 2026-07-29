export type AssistantPlanStepStatus = 'pending' | 'in_progress' | 'completed'

export interface AssistantPlanStep {
  content: string
  activeForm?: string
  status: AssistantPlanStepStatus
}

export interface AssistantPlan {
  steps: AssistantPlanStep[]
}

export interface AssistantPlanMessage {
  id: string
  role: string
  plan?: AssistantPlan
}

export interface AssistantPlanProgress {
  completed: number
  total: number
  activeLabel: string
}

const maxPlanSteps = 50
const maxPlanLabelBytes = 120
const textEncoder = new TextEncoder()

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isPlanLabel(value: unknown, required: boolean): value is string {
  return typeof value === 'string'
    && textEncoder.encode(value).byteLength <= maxPlanLabelBytes
    && (!required || value.trim().length > 0)
}

function isPlanStepStatus(value: unknown): value is AssistantPlanStepStatus {
  return value === 'pending' || value === 'in_progress' || value === 'completed'
}

export function parseAssistantPlan(value: unknown): AssistantPlan | undefined {
  if (!isRecord(value) || !Array.isArray(value.steps) || value.steps.length === 0 || value.steps.length > maxPlanSteps) {
    return undefined
  }

  let inProgress = 0
  const steps: AssistantPlanStep[] = []
  for (const valueStep of value.steps) {
    if (!isRecord(valueStep)
      || !isPlanLabel(valueStep.content, true)
      || !isPlanLabel(valueStep.activeForm ?? '', false)
      || !isPlanStepStatus(valueStep.status)) {
      return undefined
    }
    if (valueStep.activeForm !== undefined && typeof valueStep.activeForm !== 'string') {
      return undefined
    }
    if (valueStep.status === 'in_progress' && ++inProgress > 1) {
      return undefined
    }

    const step: AssistantPlanStep = { content: valueStep.content, status: valueStep.status }
    if (valueStep.activeForm !== undefined) {
      step.activeForm = valueStep.activeForm
    }
    steps.push(step)
  }

  return { steps }
}

export function assistantPlanProgress(plan: AssistantPlan): AssistantPlanProgress {
  const activeStep = plan.steps.find((step) => step.status === 'in_progress')
  return {
    completed: plan.steps.filter((step) => step.status === 'completed').length,
    total: plan.steps.length,
    activeLabel: activeStep?.activeForm || activeStep?.content || '',
  }
}

export function assistantPlanSummary(plan: AssistantPlan): string {
  const { completed, total, activeLabel } = assistantPlanProgress(plan)
  const progress = `${completed} of ${total} steps`
  if (completed === total) return progress
  const building = `Building · ${progress}`
  return activeLabel ? `${building} · ${activeLabel}` : building
}

export function assistantPlanStepStatusLabel(status: AssistantPlanStepStatus): string {
  switch (status) {
    case 'completed':
      return 'Completed'
    case 'in_progress':
      return 'In progress'
    default:
      return 'Pending'
  }
}

export function activeAssistantPlanMessage<T extends AssistantPlanMessage>(
  messages: T[],
  activeMessageID: string | undefined,
  streaming: boolean,
  activeRunTerminal: boolean,
): (T & { plan: AssistantPlan }) | undefined {
  if (!streaming || activeRunTerminal || !activeMessageID) return undefined
  const message = messages.find((item) => item.id === activeMessageID && item.role === 'assistant')
  if (!message?.plan) return undefined
  return { ...message, plan: message.plan }
}
