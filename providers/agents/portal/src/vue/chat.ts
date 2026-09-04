// Framework-neutral chat projection and markdown helpers. Keeping these outside
// the Vue components makes persisted transcript behavior independently testable
// and keeps untrusted model output behind one sanitizer.

import DOMPurify from 'dompurify'
import { Marked } from 'marked'
import type { ChatMessage, ToolCall, TranscriptMessage } from '../types'

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

// Persisted tool messages precede the assistant message they belong to. Fold
// them back into the same card shape used by the live stream; keep a trailing
// tool call visible on an orphan assistant turn if the run stopped mid-flight.
export function rebuildTranscript(chronological: TranscriptMessage[]): ChatMessage[] {
  const out: ChatMessage[] = []
  let pending: ToolCall[] = []

  const flush = (into?: ChatMessage): void => {
    if (!pending.length) return
    if (into) into.tools = pending
    else out.push({ id: `orphan-${pending[0].id}`, role: 'assistant', content: '', tools: pending })
    pending = []
  }

  for (const item of chronological) {
    if (item.role === 'tool') {
      const metadata = item.metadata || {}
      pending.push({
        id: `m${item.id}`,
        name: metadata.tool || 'tool',
        args: metadata.args,
        result: item.content,
        error: metadata.error,
        durationMS: metadata.durationMS,
        pending: false,
      })
      continue
    }
    if (item.role !== 'user' && item.role !== 'assistant') continue
    if (item.role === 'user') flush()
    const message: ChatMessage = {
      id: `m${item.id}`,
      role: item.role,
      content: item.content,
      tools: [],
      runID: item.runID,
    }
    if (item.role === 'assistant') flush(message)
    out.push(message)
  }
  flush()
  return out
}
