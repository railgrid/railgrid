// Models menu: workspace-shared model credentials (each is its own Secret,
// kedge-agents-model-<name>). Create + delete; assign to agents in each agent's
// Settings tab.

import type { ViewCtx } from '../view'
import { escapeHTML } from '../types'
import { PROVIDER_PRESETS } from '../conn-defs'
import { createCredential, deleteCredential } from '../actions'

let msg: string | null = null

export function render(vc: ViewCtx): string {
  const creds = vc.store.credentials
  return `
    <div class="agents-panel agents-form-panel">
      <h3>Models</h3>
      <p class="muted">Model credentials shared across the workspace — create once, assign to agents in each agent's Settings. Each is its own Secret (<code>kedge-agents-model-&lt;name&gt;</code>).</p>
      <table class="agents-table">
        <thead><tr><th>Name</th><th>Provider</th><th>Model</th><th>Base URL</th><th class="agents-th-actions">Actions</th></tr></thead>
        <tbody>
          ${
            creds.length
              ? creds
                  .map(
                    (c) => `<tr>
                      <td><strong>${escapeHTML(c.name)}</strong></td>
                      <td>${escapeHTML(c.provider || '')}</td>
                      <td>${escapeHTML(c.model || '')}</td>
                      <td class="muted">${escapeHTML(c.baseURL || '')}</td>
                      <td class="agents-row-actions"><button class="agents-iconbtn agents-iconbtn-danger" data-delcred="${escapeHTML(c.name)}" title="Delete">🗑</button></td>
                    </tr>`,
                  )
                  .join('')
              : `<tr class="agents-empty-row"><td colspan="5"><span class="agents-empty">⚙ No models yet — add one below.</span></td></tr>`
          }
        </tbody>
      </table>
      <form class="agents-cred-form">
        <h4>New model credential</h4>
        <div class="agents-grid2">
          <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="my-openai" /></label>
          <label>Provider<select name="preset">${PROVIDER_PRESETS.map((p) => `<option value="${p.id}">${escapeHTML(p.label)}</option>`).join('')}</select></label>
          <label>Base URL<input name="baseURL" value="${PROVIDER_PRESETS[0].baseURL}" placeholder="https://api.openai.com/v1" /></label>
          <label>Model<input name="model" placeholder="gpt-4o" required /></label>
        </div>
        <label>API key<input name="apiKey" type="password" autocomplete="off" placeholder="sk-…" required /></label>
        ${msg ? `<div class="agents-msg-note">${escapeHTML(msg)}</div>` : ''}
        <button>Add credential</button>
      </form>
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  root.querySelectorAll<HTMLElement>('[data-delcred]').forEach((el) =>
    el.addEventListener('click', () => {
      if (confirm(`Delete credential ${el.dataset.delcred}? Agents using it will need reassigning.`)) void deleteCredential(vc, el.dataset.delcred!)
    }),
  )
  const form = root.querySelector<HTMLFormElement>('.agents-cred-form')
  if (!form) return
  const preset = form.querySelector<HTMLSelectElement>('select[name=preset]')!
  const baseURL = form.querySelector<HTMLInputElement>('input[name=baseURL]')!
  const model = form.querySelector<HTMLInputElement>('input[name=model]')!
  preset.addEventListener('change', () => {
    const p = PROVIDER_PRESETS.find((x) => x.id === preset.value)
    if (p && p.id !== 'custom') baseURL.value = p.baseURL
    if (p) model.placeholder = p.modelHint
  })
  form.addEventListener('submit', (e) => {
    e.preventDefault()
    const g = (n: string) => (form.querySelector<HTMLInputElement>(`input[name=${n}]`)?.value || '').trim()
    msg = 'Saving…'
    vc.rerender()
    void createCredential(vc, { name: g('name'), provider: 'openai-compatible', baseURL: baseURL.value.trim(), model: model.value.trim(), apiKey: g('apiKey') }).then((ok) => {
      msg = ok ? 'Credential saved.' : msg
    })
  })
}
