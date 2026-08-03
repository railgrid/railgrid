import type { ProjectAssistantUIInterruptRequest } from './types'
import { parseAssistantExecDisclosure } from './assistantExecDisclosure'

/**
 * The portal's view of a server-owned interrupt. The interrupt envelope and
 * action identifiers remain usable when optional execution disclosure fails
 * validation, but the invalid disclosure is never retained in the view.
 */
export interface ProjectAssistantInterruptView extends ProjectAssistantUIInterruptRequest {
  execDisclosureInvalid?: boolean
}

/**
 * Allow is only safe when the server supplied actionable identifiers and the
 * optional execution disclosure passed strict validation. A malformed
 * disclosure must leave Deny available without authorizing an opaque action.
 */
export function assistantInterruptAllowsApproval(interrupt: ProjectAssistantInterruptView | undefined): boolean {
  return Boolean(
    interrupt
      && interrupt.action?.runId
      && interrupt.action.requestId
      && !interrupt.execDisclosureInvalid,
  )
}

const interruptKeys = new Set(['interruptId', 'kind', 'surfaceId', 'description', 'questions', 'status', 'exec', 'action'])
const actionKeys = new Set(['runId', 'requestId', 'assistantMessageId', 'exec'])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * Parse the actionable interrupt envelope while treating exec disclosure as
 * optional presentation data. Strict disclosure parsing remains unchanged;
 * an invalid disclosure is stripped and surfaced as a safe warning state so
 * that the user can still deny the request without seeing unvalidated args.
 */
export function parseAssistantInterrupt(value: unknown): ProjectAssistantInterruptView | undefined {
  if (!isRecord(value) || Object.keys(value).some((key) => !interruptKeys.has(key))) return undefined
  if (typeof value.interruptId !== 'string' || value.interruptId.trim().length === 0
    || (value.kind !== undefined && value.kind !== 'permission' && value.kind !== 'follow_up')
    || (value.status !== undefined && value.status !== 'pending' && value.status !== 'resolved')
    || (value.surfaceId !== undefined && typeof value.surfaceId !== 'string')
    || (value.description !== undefined && typeof value.description !== 'string')
    || (value.questions !== undefined && !Array.isArray(value.questions))) return undefined

  let execDisclosureInvalid = false
  const parsedExec = value.exec === undefined ? undefined : parseAssistantExecDisclosure(value.exec)
  if (value.exec !== undefined && !parsedExec) execDisclosureInvalid = true

  let action: ProjectAssistantInterruptView['action']
  if (value.action !== undefined) {
    if (!isRecord(value.action) || Object.keys(value.action).some((key) => !actionKeys.has(key))
      || typeof value.action.runId !== 'string'
      || typeof value.action.requestId !== 'string'
      || (value.action.assistantMessageId !== undefined && typeof value.action.assistantMessageId !== 'string')) return undefined

    const actionExec = value.action.exec === undefined ? undefined : parseAssistantExecDisclosure(value.action.exec)
    if (value.action.exec !== undefined && !actionExec) execDisclosureInvalid = true
    action = {
      runId: value.action.runId,
      requestId: value.action.requestId,
      ...(value.action.assistantMessageId !== undefined ? { assistantMessageId: value.action.assistantMessageId } : {}),
      ...(actionExec ? { exec: actionExec } : {}),
    }
  }

  return {
    interruptId: value.interruptId,
    ...(value.kind !== undefined ? { kind: value.kind } : {}),
    ...(value.surfaceId !== undefined ? { surfaceId: value.surfaceId } : {}),
    ...(value.description !== undefined ? { description: value.description } : {}),
    ...(value.questions !== undefined ? { questions: value.questions } : {}),
    ...(value.status !== undefined ? { status: value.status } : {}),
    ...(parsedExec ? { exec: parsedExec } : {}),
    ...(action ? { action } : {}),
    ...(execDisclosureInvalid ? { execDisclosureInvalid: true } : {}),
  }
}
