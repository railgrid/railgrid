// Framework-neutral chat projection and markdown helpers. Keeping these outside
// the Vue components makes persisted transcript behavior independently testable
// and keeps untrusted model output behind one sanitizer.

import DOMPurify from 'dompurify'
import { Marked } from 'marked'
import type { AITurnProgressStatus } from '../agentkit/conversation'
import type { ChatMessage, ChatProgress, ChatTraceBlock, ToolCall, TranscriptMessage } from '../types'

const markdown = new Marked({ gfm: true, breaks: true })

DOMPurify.addHook('afterSanitizeAttributes', node => {
  if (node instanceof HTMLAnchorElement && node.hasAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function sanitizedMarkdown(source: string): string {
  const raw = markdown.parse(source || '', { async: false })
  return DOMPurify.sanitize(raw, { USE_PROFILES: { html: true } })
}

// Copy controls are intentionally attached after sanitization rather than
// included in the model-controlled HTML. The data flag keeps repeated Vue
// updates idempotent while a response streams.
export function attachCodeCopy(root: ParentNode): void {
  root.querySelectorAll<HTMLPreElement>('.agents-body pre').forEach(pre => {
    if (pre.dataset.copyWired) return
    pre.dataset.copyWired = '1'

    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'agents-code-copy'
    button.textContent = 'Copy'
    button.setAttribute('aria-label', 'Copy code block')
    button.addEventListener('click', () => {
      const text = pre.querySelector('code')?.textContent ?? pre.textContent ?? ''
      const write = navigator.clipboard?.writeText(text)
      if (!write) {
        button.textContent = 'Failed'
        return
      }
      void write.then(
        () => {
          button.textContent = 'Copied'
          setTimeout(() => { button.textContent = 'Copy' }, 1500)
        },
        () => { button.textContent = 'Failed' },
      )
    })
    pre.appendChild(button)
  })
}

const progressStatuses = new Set<AITurnProgressStatus>([
  'pending', 'running', 'waiting', 'stopping', 'completed', 'failed', 'interrupted', 'aborted',
])

function progressStatus(value: unknown): AITurnProgressStatus | undefined {
  if (typeof value !== 'string') return undefined
  const candidate = value.trim().toLowerCase()
  if (candidate === 'pendingapproval' || candidate === 'pending_approval') return 'waiting'
  if (candidate === 'succeeded' || candidate === 'success') return 'completed'
  if (candidate === 'interrupted') return 'interrupted'
  if (candidate === 'in_progress') return 'running'
  return progressStatuses.has(candidate as AITurnProgressStatus)
    ? candidate as AITurnProgressStatus
    : undefined
}

function durationMS(value: unknown): number | undefined {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) return undefined
  return value
}

function progressFromMetadata(metadata: TranscriptMessage['metadata']): ChatProgress | undefined {
  const status = progressStatus(metadata?.turnStatus)
  if (!status) return undefined
  const startedAt = typeof metadata?.startedAt === 'string' && metadata.startedAt.trim() ? metadata.startedAt : undefined
  const duration = durationMS(metadata?.durationMS)
  return {
    status,
    ...(startedAt ? { startedAt } : {}),
    ...(duration !== undefined ? { durationMS: duration } : {}),
    trace: [],
  }
}

function applyProgressMetadata(message: ChatMessage, metadata: TranscriptMessage['metadata']): void {
  const projected = progressFromMetadata(metadata)
  if (!projected && !message.progress) return
  const current = message.progress
  if (!projected) return
  message.progress = {
    ...projected,
    trace: current?.trace || projected.trace,
  }
  if (metadata?.turnError && (projected.status === 'failed' || projected.status === 'aborted' || projected.status === 'interrupted')) {
    message.error = metadata.turnError
  }
}

function appendTrace(message: ChatMessage, block: ChatTraceBlock, metadata?: TranscriptMessage['metadata']): void {
  const projected = progressFromMetadata(metadata)
  const current = message.progress
  if (!current && !projected) return
  const trace = current?.trace ? [...current.trace] : []
  const index = trace.findIndex(candidate => candidate.id === block.id)
  if (index === -1) trace.push(block)
  else trace[index] = block
  message.progress = {
    ...(current || projected!),
    trace,
  }
}

function appendTool(message: ChatMessage, tool: ToolCall, createdAt?: string): void {
  const index = message.tools.findIndex(candidate => candidate.id === tool.id)
  if (index === -1) message.tools.push(tool)
  else message.tools[index] = { ...message.tools[index], ...tool }
  appendTrace(message, {
    id: `tool-${tool.id}`,
    kind: 'tool',
    tool: { ...tool },
    ...(createdAt ? { createdAt } : {}),
  })
}

// Persisted tool messages precede the assistant message they belong to. Fold
// them back into the same card shape used by the live stream. New assistant
// rows carry explicit phase/status metadata, so commentary and repeated
// approval markers stay attached to one turn without guessing from prose.
export function rebuildTranscript(chronological: TranscriptMessage[]): ChatMessage[] {
  const out: ChatMessage[] = []
  let pending: Array<{ tool: ToolCall; createdAt?: string }> = []
  let activeAssistant: ChatMessage | undefined

  const flush = (into?: ChatMessage): void => {
    if (!pending.length) return
    if (into) {
      for (const item of pending) appendTool(into, item.tool, item.createdAt)
    } else {
      out.push({
        id: `orphan-${pending[0].tool.id}`,
        role: 'assistant',
        content: '',
        tools: pending.map(item => item.tool),
      })
    }
    pending = []
  }

  const makeMessage = (item: TranscriptMessage, content = item.content): ChatMessage => ({
    id: `m${item.id}`,
    role: item.role === 'user' ? 'user' : 'assistant',
    content,
    tools: [],
    runID: item.runID,
    ...(item.createdAt ? { createdAt: item.createdAt } : {}),
    ...(progressFromMetadata(item.metadata) ? { progress: progressFromMetadata(item.metadata) } : {}),
  })

  const ensureAssistant = (item: TranscriptMessage, mergeExisting: boolean): ChatMessage => {
    if (mergeExisting && activeAssistant && (!item.runID || activeAssistant.runID === item.runID)) return activeAssistant
    const message = makeMessage(item, '')
    out.push(message)
    activeAssistant = message
    return message
  }

  for (const item of chronological) {
    if (item.role === 'tool') {
      const metadata = item.metadata || {}
      const rawError = metadata.error
      pending.push({
        tool: {
          id: `m${item.id}`,
          name: metadata.tool || 'tool',
          args: metadata.args,
          result: item.content,
          error: typeof rawError === 'string' ? rawError : rawError ? 'Tool failed' : undefined,
          durationMS: durationMS(metadata.durationMS),
          pending: false,
        },
        createdAt: item.createdAt,
      })
      if (activeAssistant && (!item.runID || !activeAssistant.runID || item.runID === activeAssistant.runID)) {
        flush(activeAssistant)
      }
      continue
    }
    if (item.role !== 'user' && item.role !== 'assistant') continue
    if (item.role === 'user') {
      flush()
      const message = makeMessage(item)
      out.push(message)
      activeAssistant = undefined
      continue
    }

    const phase = typeof item.metadata?.turnPhase === 'string' ? item.metadata.turnPhase.trim().toLowerCase() : ''
    if (phase === 'commentary') {
      const message = ensureAssistant(item, true)
      flush(message)
      if (item.content) {
        const status = progressStatus(item.metadata?.turnStatus)
        if (status || message.progress) {
          appendTrace(message, {
            id: `m${item.id}`,
            kind: 'commentary',
            content: item.content,
            ...(item.createdAt ? { createdAt: item.createdAt } : {}),
          }, item.metadata)
        } else {
          // A malformed metadata record must remain visible rather than being
          // silently dropped, while it still receives no inferred success.
          message.content += item.content
        }
      }
      if (item.createdAt && !message.createdAt) message.createdAt = item.createdAt
      applyProgressMetadata(message, item.metadata)
      continue
    }
    if (phase === 'terminal') {
      const message = ensureAssistant(item, true)
      flush(message)
      if (item.content && !message.content) message.content = item.content
      if (item.createdAt) message.createdAt = item.createdAt
      applyProgressMetadata(message, item.metadata)
      continue
    }
    if (phase === 'final') {
      const message = ensureAssistant(item, Boolean(activeAssistant?.progress && activeAssistant.runID === item.runID))
      flush(message)
      message.content = item.content
      if (item.createdAt) message.createdAt = item.createdAt
      applyProgressMetadata(message, item.metadata)
      continue
    }

    // Legacy assistant rows remain separate segments, preserving the old run
    // link and message ordering for transcripts written before typed phases.
    const message = makeMessage(item)
    flush(message)
    out.push(message)
    activeAssistant = message
  }
  flush()
  return out
}
