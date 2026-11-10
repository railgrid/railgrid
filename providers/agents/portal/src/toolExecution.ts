import { commandActivityPresentation } from './agentkit/activity'
import { redactTechnicalSecrets } from './failure-presentation'
import type { ToolCall } from './types'

/** Project only known shell tools; foreign tools retain their ordinary arguments. */
export function toolCommand(tool: ToolCall): string | undefined {
  if (!/^(command_?execution|exec_command|shell_command|shell|bash)$/i.test(tool.name) || !tool.args?.trim()) return undefined
  let command: unknown = tool.args
  try {
    const value: unknown = JSON.parse(tool.args)
    if (typeof value === 'string') command = value
    else if (value && typeof value === 'object' && !Array.isArray(value)) {
      const args = value as Record<string, unknown>
      command = args.command ?? args.cmd
    } else return undefined
  } catch {
    // Harness command_execution items carry the command as plain text.
    if (/^[{[]/.test(tool.args.trim())) return undefined
  }
  if (typeof command !== 'string' || !command.trim()) return undefined
  // A simple quoted shell wrapper is presentation noise. Complex quoting
  // is displayed verbatim.
  const wrapped = command.match(/^(?:\/[^\s]+\/)?(?:ba|z)?sh -[a-z]*c\s+'([^']*)'$/s)
  return redactTechnicalSecrets(wrapped?.[1] || command)
}

export function toolCommandTitle(tool: ToolCall): string | undefined {
  const command = toolCommand(tool)
  if (!command) return undefined
  const verb = toolOutcome(tool)?.kind === 'exit' ? 'Ran' : tool.pending ? 'Running' : tool.error ? 'Failed' : 'Ran'
  return `${verb} ${command.replace(/\s+/g, ' ').trim()}`
}

export function toolOutcome(tool: Pick<ToolCall, 'exitCode' | 'outcomeUnknown'>) {
  if (typeof tool.exitCode === 'number' && Number.isSafeInteger(tool.exitCode)) {
    return {
      kind: 'exit' as const,
      label: 'Completed',
      exitCode: tool.exitCode,
    }
  }
  if (tool.outcomeUnknown === true) {
    return {
      kind: 'unknown' as const,
      label: 'Outcome unknown',
      detail: 'No completion was confirmed. Check what the tool did before retrying.',
    }
  }
  return undefined
}

export function toolOutcomeLabel(outcome: ReturnType<typeof toolOutcome>): string {
  if (!outcome) return ''
  return outcome.kind === 'unknown' ? outcome.label : `${outcome.label} · exit ${outcome.exitCode}`
}

export function toolCommandActivity(command: string, status: string, statusLabel: string) {
  const activity = commandActivityPresentation(command)
  return {
    ...activity,
    title: status === 'succeeded' ? activity.title : status === 'running' ? activity.runningTitle : `${statusLabel} ${command}`,
  }
}
