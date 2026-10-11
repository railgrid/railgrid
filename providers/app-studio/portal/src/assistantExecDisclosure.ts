import type { ProjectAssistantExecDisclosure } from './types'

const textEncoder = new TextEncoder()
const execDisclosureKeys = new Set([
  'component',
  'argv',
  'workdir',
  'timeoutSeconds',
  'authorityProfile',
  'networkProfile',
  'writebackPolicy',
  'sourceRevision',
  'sourceDigest',
  'syncStatus',
  'status',
  'summary',
  'sourceStale',
  'exitCode',
  'durationMs',
  'stdout',
  'stderr',
  'outputTruncated',
  'detail',
  'detailURL',
])
const execStatuses = new Set([
  'succeeded', 'failed', 'stale', 'timed_out', 'canceled', 'cancelled', 'blocked', 'error', 'running',
  'permission_required', 'outcome_unknown', 'unverifiable', 'not_executed', 'rejected', 'skipped',
  'requested', 'pending', 'waiting', 'input_required', 'retrying', 'recovered',
])
// The server accepts source argv tokens up to 4096 bytes. Secret redaction can
// expand repeated short values (for example `passwd=x`), and the public
// projection does not truncate argv, so allow a bounded 3x public token size.
const maxPublicArgTokenBytes = 3 * 4096
const maxOutputBytes = 1 << 20

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function boundedString(value: unknown, maxBytes: number, required = false): value is string {
  return typeof value === 'string'
    && !value.includes('\u0000')
    && textEncoder.encode(value).byteLength <= maxBytes
    && (!required || value.trim().length > 0)
}

function boundedCodePointString(value: unknown, maxCodePoints: number, required = false): value is string {
  return boundedString(value, maxCodePoints * 4, required) && [...value].length <= maxCodePoints
}

function hasOnlyKeys(value: Record<string, unknown>): boolean {
  return Object.keys(value).every((key) => execDisclosureKeys.has(key))
}

function validRelativeWorkdir(value: string): boolean {
  if (value === '' || value === '.') return true
  if (value.startsWith('/') || value.startsWith('\\') || /^[A-Za-z]:[\\/]/.test(value)) return false
  return !value.split(/[\\/]+/).some((segment) => segment === '..')
}

function parseArgv(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length < 1 || value.length > 32) return undefined
  if (value.some((token) => !boundedString(token, maxPublicArgTokenBytes, true))) return undefined
  return value as string[]
}

function parseOutput(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length > 200) return undefined
  let bytes = 0
  for (const line of value) {
    if (!boundedCodePointString(line, 4_096)) return undefined
    bytes += textEncoder.encode(line).byteLength
    if (bytes > maxOutputBytes) return undefined
  }
  return value as string[]
}

function parseDetailURL(value: unknown): string | undefined {
  if (!boundedString(value, 512, true)) return undefined
  // The disclosure is server-authored, but links still stay same-origin. Do
  // not let an activity payload turn into an external or javascript link.
  if (!value.startsWith('/') || value.startsWith('//') || /[\u0000\r\n]/.test(value)) return undefined
  return value
}

/**
 * Parse the intentionally small, server-owned exec disclosure contract.
 * Unknown keys invalidate the disclosure so raw tool arguments cannot leak
 * into the portal by accident.
 */
export function parseAssistantExecDisclosure(value: unknown): ProjectAssistantExecDisclosure | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value)) return undefined

  const component = value.component === undefined ? undefined : value.component
  if (component !== undefined && !boundedString(component, 64, true)) return undefined

  const argv = value.argv === undefined ? undefined : parseArgv(value.argv)
  if (value.argv !== undefined && !argv) return undefined

  const workdir = value.workdir === undefined ? undefined : value.workdir
  if (workdir !== undefined && (!boundedString(workdir, 256) || !validRelativeWorkdir(workdir))) return undefined

  let timeoutSeconds: number | undefined
  if (value.timeoutSeconds !== undefined) {
    if (!Number.isSafeInteger(value.timeoutSeconds) || Number(value.timeoutSeconds) < 1 || Number(value.timeoutSeconds) > 120) return undefined
    timeoutSeconds = Number(value.timeoutSeconds)
  }

  const authorityProfile = value.authorityProfile === undefined ? undefined : value.authorityProfile
  if (authorityProfile !== undefined && !boundedString(authorityProfile, 64, true)) return undefined

  const networkProfile = value.networkProfile === undefined ? undefined : value.networkProfile
  if (networkProfile !== undefined && !boundedString(networkProfile, 64, true)) return undefined

  const writebackPolicy = value.writebackPolicy === undefined ? undefined : value.writebackPolicy
  if (writebackPolicy !== undefined && !boundedString(writebackPolicy, 80, true)) return undefined

  const status = value.status === undefined ? undefined : value.status
  if (status !== undefined && (typeof status !== 'string' || !execStatuses.has(status))) return undefined

  const sourceRevision = value.sourceRevision === undefined ? undefined : value.sourceRevision
  if (sourceRevision !== undefined && (!Number.isSafeInteger(sourceRevision) || (sourceRevision as number) < 0)) return undefined

  const sourceDigest = value.sourceDigest === undefined ? undefined : value.sourceDigest
  if (sourceDigest !== undefined && !boundedString(sourceDigest, 256, true)) return undefined

  const syncStatus = value.syncStatus === undefined ? undefined : value.syncStatus
  if (syncStatus !== undefined && !boundedString(syncStatus, 64, true)) return undefined

  const summary = value.summary === undefined ? undefined : value.summary
  if (summary !== undefined && !boundedCodePointString(summary, 240, true)) return undefined

  const sourceStale = value.sourceStale === undefined ? undefined : value.sourceStale
  if (sourceStale !== undefined && typeof sourceStale !== 'boolean') return undefined

  let exitCode: number | null | undefined
  if (value.exitCode === null) {
    exitCode = null
  } else if (value.exitCode !== undefined) {
    if (!Number.isSafeInteger(value.exitCode) || (value.exitCode as number) < -1 || (value.exitCode as number) > 255) return undefined
    exitCode = value.exitCode as number
  }

  let durationMs: number | undefined
  if (value.durationMs !== undefined) {
    if (!Number.isSafeInteger(value.durationMs) || (value.durationMs as number) < 0 || (value.durationMs as number) > 7 * 24 * 60 * 60 * 1000) return undefined
    durationMs = value.durationMs as number
  }

  const stdout = value.stdout === undefined ? undefined : parseOutput(value.stdout)
  if (value.stdout !== undefined && !stdout) return undefined
  const stderr = value.stderr === undefined ? undefined : parseOutput(value.stderr)
  if (value.stderr !== undefined && !stderr) return undefined

  const outputTruncated = value.outputTruncated === undefined ? undefined : value.outputTruncated
  if (outputTruncated !== undefined && typeof outputTruncated !== 'boolean') return undefined

  const detail = value.detail === undefined ? undefined : value.detail
  if (detail !== undefined && !boundedString(detail, 240, true)) return undefined
  const detailURL = value.detailURL === undefined ? undefined : parseDetailURL(value.detailURL)
  if (value.detailURL !== undefined && !detailURL) return undefined

  if (component === undefined && argv === undefined && workdir === undefined && timeoutSeconds === undefined
    && authorityProfile === undefined && networkProfile === undefined && writebackPolicy === undefined && status === undefined && summary === undefined && sourceStale === undefined
    && sourceRevision === undefined && sourceDigest === undefined && syncStatus === undefined
    && exitCode === undefined && durationMs === undefined && stdout === undefined && stderr === undefined
    && outputTruncated === undefined && detail === undefined && detailURL === undefined) return undefined

  return {
    ...(component !== undefined ? { component } : {}),
    ...(argv !== undefined ? { argv } : {}),
    ...(workdir !== undefined ? { workdir } : {}),
    ...(timeoutSeconds !== undefined ? { timeoutSeconds } : {}),
    ...(authorityProfile !== undefined ? { authorityProfile } : {}),
    ...(networkProfile !== undefined ? { networkProfile } : {}),
    ...(writebackPolicy !== undefined ? { writebackPolicy } : {}),
    ...(sourceRevision !== undefined ? { sourceRevision: sourceRevision as number } : {}),
    ...(sourceDigest !== undefined ? { sourceDigest } : {}),
    ...(syncStatus !== undefined ? { syncStatus } : {}),
    ...(status !== undefined ? { status } : {}),
    ...(summary !== undefined ? { summary } : {}),
    ...(sourceStale !== undefined ? { sourceStale } : {}),
    ...(exitCode !== undefined ? { exitCode } : {}),
    ...(durationMs !== undefined ? { durationMs } : {}),
    ...(stdout !== undefined ? { stdout } : {}),
    ...(stderr !== undefined ? { stderr } : {}),
    ...(outputTruncated !== undefined ? { outputTruncated } : {}),
    ...(detail !== undefined ? { detail } : {}),
    ...(detailURL !== undefined ? { detailURL } : {}),
  }
}

export function formatAssistantExecCommand(exec?: ProjectAssistantExecDisclosure): string {
  if (!exec?.argv?.length) return ''
  return exec.argv.map((token) => /[\s"'\\]/.test(token) ? JSON.stringify(token) : token).join(' ')
}

export type AssistantExecDisplayState = 'running' | 'ran' | 'failed' | 'canceled' | 'timed_out' | 'blocked' | 'unverified' | 'skipped'

export interface AssistantExecStatusPresentation {
  state: AssistantExecDisplayState
  label: 'Running' | 'Ran' | 'Failed' | 'Canceled' | 'Timed out' | 'Blocked' | 'Unverified' | 'Skipped'
  busy: boolean
  error: boolean
  attention: boolean
}

/**
 * Resolve the user-facing execution state from the nested, server-owned exec
 * disclosure. The action-feed status describes the tool lifecycle and can be
 * settled before the command itself has reported its terminal result, so it
 * must only be a fallback when the nested status is absent.
 */
export function assistantExecStatusPresentation(
  exec?: ProjectAssistantExecDisclosure,
  fallbackStatus?: string,
): AssistantExecStatusPresentation {
  const nestedStatus = exec?.status
  const status = nestedStatus || fallbackStatus
  const nonZeroExit = exec?.exitCode !== undefined && exec.exitCode !== null && exec.exitCode !== 0
  const hasExitCode = exec?.exitCode !== undefined && exec.exitCode !== null
  let state: AssistantExecDisplayState
  if (nonZeroExit && !['failed', 'error', 'timed_out', 'canceled', 'cancelled', 'blocked', 'permission_required', 'rejected', 'not_executed'].includes(status || '')) {
    state = 'failed'
  } else if (exec?.sourceStale && !['failed', 'error', 'timed_out', 'canceled', 'cancelled', 'blocked', 'permission_required', 'rejected', 'not_executed'].includes(status || '')
    && !nonZeroExit) {
    state = 'unverified'
  } else {
    switch (status) {
      case 'running':
      case 'requested':
      case 'pending':
      case 'retrying':
        state = 'running'
        break
      case 'failed':
      case 'error':
        state = 'failed'
        break
      case 'canceled':
      case 'cancelled':
        state = 'canceled'
        break
      case 'timed_out':
        state = 'timed_out'
        break
      case 'blocked':
      case 'permission_required':
      case 'waiting':
      case 'rejected':
      case 'not_executed':
      case 'input_required':
        state = 'blocked'
        break
      case 'stale':
      case 'outcome_unknown':
      case 'unverifiable':
        state = 'unverified'
        break
      case 'skipped':
        state = 'skipped'
        break
      case 'succeeded':
      case 'recovered':
        // A success label on either the nested receipt or outer action is not
        // terminal process proof. Only a reported exit code can establish Ran.
        state = hasExitCode ? 'ran' : 'unverified'
        break
      default:
        // A non-zero exit is enough to establish failure. Missing or future
        // status values without an exit code are never presented as success.
        state = !nestedStatus && !status && hasExitCode && exec?.exitCode === 0 ? 'ran' : 'unverified'
        break
    }
  }

  switch (state) {
    case 'running':
      return { state, label: 'Running', busy: true, error: false, attention: false }
    case 'failed':
      return { state, label: 'Failed', busy: false, error: true, attention: false }
    case 'timed_out':
      return { state, label: 'Timed out', busy: false, error: true, attention: false }
    case 'blocked':
      return { state, label: 'Blocked', busy: false, error: false, attention: true }
    case 'unverified':
      return { state, label: 'Unverified', busy: false, error: false, attention: true }
    case 'skipped':
      return { state, label: 'Skipped', busy: false, error: false, attention: false }
    case 'canceled':
      return { state, label: 'Canceled', busy: false, error: false, attention: false }
    default:
      return { state: 'ran', label: 'Ran', busy: false, error: false, attention: false }
  }
}

export function formatAssistantExecDuration(durationMs?: number): string {
  if (durationMs === undefined) return ''
  if (durationMs < 1_000) return `${durationMs} ms`
  const seconds = durationMs / 1_000
  if (seconds < 60) return `${seconds.toFixed(seconds >= 10 ? 0 : 1)} s`
  const minutes = Math.floor(seconds / 60)
  const remainder = Math.round(seconds % 60)
  return `${minutes}m ${remainder}s`
}

export function formatAssistantExecExit(exec?: ProjectAssistantExecDisclosure): string {
  if (!exec) return ''
  if (exec.exitCode !== undefined && exec.exitCode !== null) return `exit ${exec.exitCode}`
  return exec.status ? exec.status.replace(/_/g, ' ') : ''
}
