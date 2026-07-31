// One chat turn: the message bubble, its tool-call cards, an approval card when
// the run paused on a gate, and the per-turn usage footer.
//
// It is its own element so a streaming delta re-renders exactly one node — the
// parent hands it a fresh message object and lit updates only that child.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { LightElement } from '../ui/base'
import { icon } from '../ui/icon'
import { attachCodeCopy, renderMarkdown } from '../ui/markdown'
import { fmtDuration, fmtTokens, fmtUSD, prettyJSON, type ChatMessage, type ToolCall } from '../types'

export class ChatMessageView extends LightElement {
  @property({ attribute: false }) message!: ChatMessage
  @state() private expanded = new Set<string>()

  private toggle(id: string): void {
    const next = new Set(this.expanded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    this.expanded = next
  }

  protected updated(): void {
    // Copy buttons are chrome around sanitized markdown, so they're attached
    // after render rather than baked into the sanitized HTML.
    attachCodeCopy(this)
  }

  render(): TemplateResult {
    const m = this.message
    return html`<div class="agents-msg ${m.role}">
      <div class="agents-role">${m.role}</div>
      ${m.tools.length ? html`<div class="agents-toolcards">${m.tools.map((t) => this.toolCard(t))}</div>` : nothing}
      ${m.content || !m.streaming
        ? html`<div class="agents-body">
            ${m.role === 'assistant' ? renderMarkdown(m.content) : m.content}
            ${m.streaming ? html`<span class="agents-caret" aria-hidden="true"></span>` : nothing}
          </div>`
        : html`<div class="agents-body agents-thinking"><span class="agents-dots" aria-hidden="true"></span> thinking…</div>`}
      ${m.approval ? this.approvalCard(m.approval) : nothing}
      ${m.error ? html`<div class="agents-err" role="alert">${m.error}</div>` : nothing}
      ${m.usage
        ? html`<div class="agents-turn-usage">
            ${fmtTokens(m.usage.inputTokens)} in · ${fmtTokens(m.usage.outputTokens)} out · ${fmtUSD(m.usage.usdMicros)}
          </div>`
        : nothing}
    </div>`
  }

  private toolCard(t: ToolCall): TemplateResult {
    const open = this.expanded.has(t.id)
    const state = t.pending ? 'pending' : t.error ? 'err' : 'ok'
    return html`<div class="agents-toolcard is-${state}">
      <button
        class="agents-toolcard-head"
        aria-expanded=${open ? 'true' : 'false'}
        @click=${() => this.toggle(t.id)}
      >
        <span class="agents-toolcard-ic">${t.pending ? html`<span class="agents-spinner" aria-hidden="true"></span>` : icon(t.error ? 'x' : 'check')}</span>
        <span class="agents-toolcard-name mono">${t.name}</span>
        <span class="agents-toolcard-meta">
          ${t.pending ? 'running…' : t.error ? 'failed' : 'ok'}${t.durationMS ? ` · ${fmtDuration(t.durationMS)}` : ''}
        </span>
        <span class="agents-toolcard-chev ${open ? 'open' : ''}">${icon('chevron-right')}</span>
      </button>
      ${open
        ? html`<div class="agents-toolcard-body">
            ${t.args ? html`<div class="agents-kv"><span>args</span><pre>${prettyJSON(t.args)}</pre></div>` : nothing}
            ${t.error
              ? html`<div class="agents-kv"><span>error</span><pre class="err">${t.error}</pre></div>`
              : t.result
                ? html`<div class="agents-kv"><span>result</span><pre>${prettyJSON(t.result)}</pre></div>`
                : nothing}
          </div>`
        : nothing}
    </div>`
  }

  private approvalCard(a: NonNullable<ChatMessage['approval']>): TemplateResult {
    const decide = (decision: 'approve' | 'deny') =>
      this.dispatchEvent(
        new CustomEvent('agents-approval', { detail: { inboxID: a.inboxID, decision }, bubbles: true, composed: true }),
      )
    return html`<div class="agents-approval" role="group" aria-label="Tool approval required">
      <div class="agents-approval-head">${icon('key')} Approval required — <span class="mono">${a.tool}</span></div>
      ${a.args ? html`<pre class="agents-approval-args">${prettyJSON(a.args)}</pre>` : nothing}
      ${a.resolved
        ? html`<div class="agents-approval-done">
            ${a.resolved === 'approve' ? 'Approved — the run is resuming.' : 'Denied — the agent was told no.'}
          </div>`
        : html`<div class="agents-approval-actions">
            <button @click=${() => decide('approve')}>${icon('check')} Approve</button>
            <button class="secondary" @click=${() => decide('deny')}>${icon('x')} Deny</button>
          </div>`}
    </div>`
  }
}

if (!customElements.get('agents-chat-message')) customElements.define('agents-chat-message', ChatMessageView)
