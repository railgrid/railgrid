// Agent settings tab: displayName, model credential, system prompt, autonomy,
// monthly budget, and delegation. Tools/toolsets are wired in the Flow tab — the
// save here deliberately omits them so it never clobbers tool policy.

import { ic } from '../icons'
import type { ViewCtx } from '../view'
import type { Agent } from '../types'
import { escapeHTML } from '../types'
import { updateAgent } from '../actions'

export function render(vc: ViewCtx, a: Agent): string {
  const credOptions =
    `<option value="">— no model —</option>` +
    vc.store.credentials.map((c) => `<option value="${escapeHTML(c.name)}" ${c.name === a.spec?.models?.chat ? 'selected' : ''}>${escapeHTML(c.name)}${c.model ? ` (${escapeHTML(c.model)})` : ''}</option>`).join('')
  const autonomy = a.spec?.autonomy || 'ask'
  const others = vc.store.agents.filter((x) => x.metadata.name !== a.metadata.name)
  const delegates = new Set(a.spec?.delegates || [])
  return `
    <div class="agents-panel agents-form-panel">
      <form class="agents-settings-form">
        <label>Display name<input name="displayName" value="${escapeHTML(a.spec?.displayName || a.metadata.name)}" /></label>
        <label>Model credential
          <select name="modelCredential">${credOptions}</select>
          ${vc.store.credentials.length === 0 ? `<span class="muted" style="font-size:12px">No models yet — add one under ${ic('settings')} Models.</span>` : ''}
        </label>
        <label>System prompt (persona + standing instructions)
          <textarea name="systemPrompt" rows="4" placeholder="You are a concise assistant that…">${escapeHTML(a.spec?.systemPrompt || '')}</textarea>
        </label>
        <div class="agents-grid2">
          <label>Autonomy
            <select name="autonomy">
              ${['suggest', 'ask', 'auto'].map((v) => `<option value="${v}" ${v === autonomy ? 'selected' : ''}>${v}</option>`).join('')}
            </select>
          </label>
          <label>Monthly budget (USD, blank = unlimited)
            <input name="budgetUSD" inputmode="decimal" value="${escapeHTML(a.spec?.budget?.usdLimit || '')}" placeholder="e.g. 20" />
          </label>
        </div>
        <fieldset class="agents-tools"><legend>Tools</legend>
          <p class="agents-hint">Tools &amp; toolsets are wired in the <strong>Flow</strong> tab.</p>
        </fieldset>
        ${
          others.length
            ? `<fieldset class="agents-delegates"><legend>Can delegate to</legend>${others
                .map(
                  (o) =>
                    `<label class="agents-check"><input type="checkbox" name="delegate" value="${escapeHTML(o.metadata.name)}" ${delegates.has(o.metadata.name) ? 'checked' : ''} /> ${escapeHTML(o.metadata.name)}</label>`,
                )
                .join('')}</fieldset>`
            : ''
        }
        <div><button>Save settings</button></div>
      </form>
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement, a: Agent): void {
  const f = root.querySelector<HTMLFormElement>('.agents-settings-form')
  f?.addEventListener('submit', (e) => {
    e.preventDefault()
    const g = (n: string) => (f.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[name=${n}]`)?.value || '').trim()
    const delegates = Array.from(f.querySelectorAll<HTMLInputElement>('input[name=delegate]:checked')).map((el) => el.value)
    // Tool families/toolsets are edited in Flow — not sent here, so this save
    // never clobbers them.
    void updateAgent(vc, a.metadata.name, {
      displayName: g('displayName'),
      modelCredential: g('modelCredential'),
      systemPrompt: g('systemPrompt'),
      autonomy: g('autonomy'),
      budgetUSD: g('budgetUSD'),
      delegates,
    })
  })
}
