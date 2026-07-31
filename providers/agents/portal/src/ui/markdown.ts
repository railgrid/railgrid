// Markdown rendering for chat messages and run output.
//
// marked parses, DOMPurify sanitizes — both bundled (the portal ships one
// self-contained main.js; a CDN would break the hub's CSP). Model output is
// untrusted text, so the sanitize step is not optional: a reply containing
// <img onerror> must not execute.

import { html, type TemplateResult } from 'lit'
import { unsafeHTML } from 'lit/directives/unsafe-html.js'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'

const marked = new Marked({ gfm: true, breaks: true })

// Links open in a new tab and can't reach back into the portal's window.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node instanceof HTMLAnchorElement && node.hasAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function renderMarkdown(src: string): TemplateResult {
  const raw = marked.parse(src || '', { async: false })
  const clean = DOMPurify.sanitize(raw, { USE_PROFILES: { html: true } })
  return html`${unsafeHTML(clean)}`
}

// attachCodeCopy adds a copy button to code blocks inside RENDERED MARKDOWN
// (.agents-body) — deliberately not to every <pre>, since tool-call args and
// approval payloads use <pre> too and are not code the user wants to copy as a
// snippet. It runs after each update, idempotent via the data flag, because the
// button is chrome around sanitized HTML rather than part of it.
export function attachCodeCopy(root: ParentNode): void {
  root.querySelectorAll<HTMLPreElement>('.agents-body pre').forEach((pre) => {
    if (pre.dataset.copyWired) return
    pre.dataset.copyWired = '1'
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = 'agents-code-copy'
    btn.textContent = 'Copy'
    btn.setAttribute('aria-label', 'Copy code block')
    btn.addEventListener('click', () => {
      const text = pre.querySelector('code')?.textContent ?? pre.textContent ?? ''
      void navigator.clipboard?.writeText(text).then(
        () => {
          btn.textContent = 'Copied'
          setTimeout(() => (btn.textContent = 'Copy'), 1500)
        },
        () => (btn.textContent = 'Failed'),
      )
    })
    pre.appendChild(btn)
  })
}
