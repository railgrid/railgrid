import type { ProjectAssistantRunMode, ProjectMessage } from './types'

export interface AssistantRun {
  id: string
  status: 'pending_permission' | 'pending_input' | 'running' | 'stopping' | 'completed' | 'failed' | 'interrupted' | 'aborted'
  mode: ProjectAssistantRunMode
  revision: number
  activeMessageID: string
  clientRequestID?: string
  userMessageID?: string
  error?: { message: string; errorInfo?: string }
  requestID?: string
  abortReason?: 'interrupted' | 'replaced' | 'budget_limited' | 'iteration_limited'
}

export interface AssistantSnapshot {
  run: AssistantRun
  message: ProjectMessage
}

export interface AssistantRunStartRequest {
  content: string
  clientRequestID: string
  collaborationMode: ProjectAssistantRunMode
  expectedRunID?: string
}

export interface ConversationState<TMessage extends ProjectMessage = ProjectMessage> {
  messages: TMessage[]
  runs: Record<string, AssistantRun>
}

export interface PendingFirstProjectSubmission {
  content: string
  clientRequestID: string
  projectName: string
}

export function newFirstProjectSubmission(content: string, clientRequestID: string): PendingFirstProjectSubmission {
  return { content, clientRequestID, projectName: '' }
}

export function firstProjectSubmissionWithProject(submission: PendingFirstProjectSubmission, projectName: string): PendingFirstProjectSubmission {
  return { ...submission, projectName }
}

export function firstProjectStartPlan(submission: PendingFirstProjectSubmission) {
  return {
    createProject: !submission.projectName,
    projectName: submission.projectName,
    content: submission.content,
    clientRequestID: submission.clientRequestID,
  }
}

export function assistantRunStartPayload(content: string, clientRequestID: string, collaborationMode: ProjectAssistantRunMode = 'default') {
  return { content, clientRequestID, collaborationMode }
}

export function assistantRunStartFingerprint(projectName: string, request: Omit<AssistantRunStartRequest, 'clientRequestID'>): string {
  return JSON.stringify([
    projectName,
    request.content,
    request.collaborationMode,
    request.expectedRunID ?? '',
  ])
}

export function assistantRunMatchesStartRequest(run: AssistantRun | undefined, request: AssistantRunStartRequest): boolean {
  if (!run || run.clientRequestID !== request.clientRequestID) return false
  return run.mode === request.collaborationMode
}

export function firstProjectSubmissionAccepted(submission: PendingFirstProjectSubmission, user: Pick<ProjectMessage, 'id' | 'content'> | undefined): boolean {
	return Boolean(user?.id && user.content === submission.content)
}

export function firstProjectSubmissionMatches(submission: PendingFirstProjectSubmission | null | undefined, projectName: string, content: string): submission is PendingFirstProjectSubmission {
	return Boolean(submission && submission.projectName === projectName && submission.content === content)
}

export function firstProjectSubmissionIsCurrent(submission: PendingFirstProjectSubmission, generation: number, currentGeneration: number, selectedProject: string, routeProject: string, draftProject: string): boolean {
	return generation === currentGeneration && selectedProject === (submission.projectName || draftProject) &&
		(routeProject === submission.projectName || (!submission.projectName && routeProject === ''))
}

export function normalizeAssistantRunStatus(status: unknown): AssistantRun['status'] | undefined {
  if (typeof status !== 'string') return undefined
  const normalized = status.trim().toLowerCase()
  switch (normalized) {
    case 'pending_permission':
    case 'pending_input':
    case 'running':
    case 'stopping':
    case 'completed':
    case 'failed':
    case 'interrupted':
    case 'aborted':
      return normalized
    default:
      return undefined
  }
}

export function assistantRunTerminal(status: unknown): boolean {
  switch (normalizeAssistantRunStatus(status)) {
    case 'completed':
    case 'failed':
    case 'interrupted':
    case 'aborted':
      return true
    default:
      return false
  }
}

export function assistantRunRequiresLiveControls(run: AssistantRun | null | undefined): run is AssistantRun {
  return Boolean(run && !assistantRunTerminal(run.status))
}

export function assistantRunCanImplementPlan(run: AssistantRun | null | undefined): run is AssistantRun {
  return Boolean(
    run &&
    run.mode === 'plan' &&
    normalizeAssistantRunStatus(run.status) === 'completed' &&
    !run.error?.message?.trim(),
  )
}

export type AssistantInterruptTransition = 'approval.requested' | 'input.requested' | 'approval.resolved' | 'input.resolved'

/**
 * Apply the durable Q&A/approval lifecycle to local run controls. Stream
 * reconnects can replay a request or its resolution, so already-applied
 * transitions are idempotent and do not manufacture revisions.
 */
export function reconcileAssistantRunInterrupt(
  run: AssistantRun,
  transition: AssistantInterruptTransition,
  requestID = '',
): AssistantRun {
  if (assistantRunTerminal(run.status)) return run
  const normalizedRequestID = requestID.trim()
  const pendingStatus = transition === 'approval.requested'
    ? 'pending_permission'
    : transition === 'input.requested'
    ? 'pending_input'
    : undefined
  if (pendingStatus) {
    if (run.status === pendingStatus && run.requestID === (normalizedRequestID || undefined)) return run
    return { ...run, status: pendingStatus, requestID: normalizedRequestID || undefined, revision: run.revision + 1 }
  }
  // A resolution for an older request must not reopen a newer pending
  // request. Missing request IDs are also ambiguous while one is pending;
  // retain the pending state until a matching durable resolution arrives.
  if (run.requestID && run.requestID !== normalizedRequestID) return run
  if (run.status === 'running' && !run.requestID) return run
  return { ...run, status: 'running', requestID: undefined, revision: run.revision + 1 }
}

export function reconcileAssistantRunTerminal(
  run: AssistantRun,
  status: Extract<AssistantRun['status'], 'completed' | 'failed' | 'interrupted' | 'aborted'>,
): AssistantRun {
  if (run.status === status) return run
  return { ...run, status, requestID: undefined, revision: run.revision + 1 }
}

// Control hydration is deliberately separate from message merge: a reload may
// receive the same durable revision after local UI state was discarded.
export function canHydrateConversationRun(current: AssistantRun | undefined, incoming: AssistantRun): boolean {
  if (!current) return true
  if (incoming.revision < current.revision) return false
  return !(assistantRunTerminal(current.status) && !assistantRunTerminal(incoming.status) && incoming.revision === current.revision)
}

export function acceptConversationSnapshot(current: AssistantRun | undefined, incoming: AssistantRun): { accepted: boolean; current: AssistantRun | undefined } {
  if (!canHydrateConversationRun(current, incoming)) return { accepted: false, current }
  return { accepted: true, current: incoming }
}

// A project can have more than one run over its lifetime. Once this tab has
// accepted a run, a delayed latest response or buffered stream from a different
// run must not replace its global controls.
export function acceptScopedConversationSnapshot(
  selectedProject: string,
  currentProject: string,
  current: AssistantRun | undefined,
  incomingProject: string,
  incoming: AssistantRun,
  source: 'stream' | 'start' | 'latest' = 'stream',
  expectedRunID = '',
): { accepted: boolean; current: AssistantRun | undefined } {
  if (!selectedProject || selectedProject !== incomingProject) return { accepted: false, current }
  if (current && currentProject === incomingProject && current.id !== incoming.id) {
    if (source !== 'start' && !(source === 'latest' && current.id === expectedRunID)) return { accepted: false, current }
	return acceptConversationSnapshot(undefined, incoming)
  }
  return acceptConversationSnapshot(current, incoming)
}

export function abortedConversationSnapshot(snapshot: AssistantSnapshot): AssistantSnapshot {
  return {
    run: { ...snapshot.run, status: 'interrupted', revision: snapshot.run.revision + 1 },
    message: { ...snapshot.message, metadata: { ...snapshot.message.metadata, assistantStatus: 'Interrupted', assistantProvisional: false } },
  }
}

export function normalizeSnapshotMessage(message: ProjectMessage & { projectName?: string }): ProjectMessage {
  return { ...message, projectID: message.projectID || message.projectName || '' }
}

// Snapshot messages are authoritative and keyed by their durable IDs. Revisions
// make reconnects and simultaneous browser tabs safe: stale snapshots are ignored.
export function mergeConversationSnapshot<TMessage extends ProjectMessage>(
  state: ConversationState<TMessage>,
  snapshot: AssistantSnapshot,
): ConversationState<TMessage> {
  const previous = state.runs[snapshot.run.id]
  if (previous && snapshot.run.revision <= previous.revision) return state
  const index = state.messages.findIndex((item) => item.id === snapshot.message.id)
  const messages = [...state.messages]
  if (index < 0) messages.push(snapshot.message as TMessage)
  else messages[index] = snapshot.message as TMessage
  return { messages, runs: { ...state.runs, [snapshot.run.id]: snapshot.run } }
}

export function replaceOptimisticUserMessage<TMessage extends ProjectMessage>(
  messages: TMessage[],
  optimisticID: string,
  persisted: ProjectMessage,
): TMessage[] {
  const withoutPersisted = messages.filter((item) => item.id !== persisted.id)
  const index = withoutPersisted.findIndex((item) => item.id === optimisticID)
  if (index < 0) return [...withoutPersisted, persisted as TMessage]
  const next = [...withoutPersisted]
  next[index] = persisted as TMessage
  return next
}

// Durable turns historically persisted the user message and assistant
// placeholder with the same timestamp. The store's random-ID tie-break can
// therefore return either role first after a reload. Keep chronological order,
// but restore the turn order for those exact timestamp ties.
export function orderConversationMessages<TMessage extends ProjectMessage>(messages: TMessage[]): TMessage[] {
  return [...messages].sort((left, right) => {
    const leftAt = Date.parse(left.createdAt)
    const rightAt = Date.parse(right.createdAt)
    if (Number.isFinite(leftAt) && Number.isFinite(rightAt) && leftAt !== rightAt) return leftAt - rightAt
    if (left.createdAt !== right.createdAt) return 0
    if (left.role === right.role) return 0
    return left.role === 'user' ? -1 : 1
  })
}

interface ConversationRunTransport {
  connect(runID: string, afterRevision: number, setDisconnect: (disconnect: () => void) => void): Promise<void>
  abort(runID: string): Promise<void>
  recover?(): Promise<void>
  setTimeout(fn: () => void, delay: number): ReturnType<typeof setTimeout>
  clearTimeout(timer: ReturnType<typeof setTimeout>): void
}

export class ConversationRunController {
  private runID = ''
  private revision = 0
  private retry = 0
  private retryTimer: ReturnType<typeof setTimeout> | undefined
  private disconnected = false
  private disconnectStream: (() => void) | undefined
  private generation = 0

  constructor(private readonly transport: ConversationRunTransport) {}

  start(runID: string, revision: number) {
    this.disconnect()
    this.generation++
    this.runID = runID
    this.revision = revision
    this.retry = 0
    this.disconnected = false
    void this.connect(this.generation)
  }

  setRevision(revision: number) { this.revision = Math.max(this.revision, revision) }
  markHealthySnapshot(revision: number) {
    this.setRevision(revision)
    this.retry = 0
  }
  setDisconnect(disconnect: () => void) { this.disconnectStream = disconnect }

  disconnect() {
    this.disconnected = true
    if (this.retryTimer !== undefined) this.transport.clearTimeout(this.retryTimer)
    this.retryTimer = undefined
    this.disconnectStream?.()
    this.disconnectStream = undefined
  }

  async stop() {
    if (!this.runID) return
    let aborted = false
    try {
      await this.transport.abort(this.runID)
      aborted = true
    } finally {
      this.disconnect()
    }
    if (aborted) {
      try {
        await this.transport.recover?.()
      } catch {
        // The abort response is authoritative. Recovery only updates local UI.
      }
    }
  }

  private async connect(generation: number) {
    if (this.disconnected || generation !== this.generation || !this.runID) return
    const runID = this.runID
    const revision = this.revision
    try {
      await this.transport.connect(runID, revision, (disconnect) => {
        if (this.disconnected || generation !== this.generation) {
          disconnect()
          return
        }
        this.setDisconnect(disconnect)
      })
      if (this.disconnected || generation !== this.generation) return
      this.scheduleReconnect(generation)
    } catch {
      if (this.disconnected || generation !== this.generation) return
      this.scheduleReconnect(generation)
    }
  }

  private scheduleReconnect(generation: number) {
    const delay = Math.min(1_000 * 2 ** this.retry, 10_000)
    this.retry++
    this.retryTimer = this.transport.setTimeout(() => { void this.connect(generation) }, delay)
  }
}
