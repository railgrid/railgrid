// Connections menu: shared credentials for external systems. Each is a ${ic('wrench')} Tool
// agents call, a ${ic('megaphone')} Channel they message you on, or a ${ic('plug')} generic Connection.
// Type-driven create (CONN_DEFS), edit with Discord special-casing, test /
// enable-inbound / OAuth-connect actions.

import { confirmModal } from '../portalkit/modal'
import { ic } from '../portalkit/icons'
import type { ViewCtx } from '../view'
import type { Connection } from '../types'
import { escapeHTML } from '../types'
import {
  CONN_DEFS,
  CATEGORY_META,
  connCategory,
  type ConnCategory,
  type ConnField,
  type ConnTypeDef,
} from '../conn-defs'
import { createConnection, updateConnection, deleteConnection, testConnection, oauthConnect, enableInbound } from '../actions'

// View-local UI state (survives re-renders; the element lives for the app life).
let connType: string | null = null
let connMode = ''
let connEdit: string | null = null

function connFieldHTML(f: ConnField): string {
  return `<label>${escapeHTML(f.label)}${f.required ? ' *' : ''}
    <input name="${f.key}" ${f.password ? 'type="password"' : ''} placeholder="${escapeHTML(f.placeholder || '')}" ${f.required ? 'required' : ''} autocomplete="off" />
    ${f.hint ? `<span class="agents-hint">${escapeHTML(f.hint)}</span>` : ''}
  </label>`
}

function renderConnEditForm(c: Connection): string {
  const cat = connCategory(c.spec.type)
  const usesChannel = cat === 'channel' || !!c.spec.channel
  // Discord is one backend type with two shapes: a webhook (channel is a
  // https:// URL, no secret) or a chat bot (channel is a numeric id or blank,
  // secret is the bot token). Detect so editing shows the right form.
  const isDiscord = c.spec.type === 'discord'
  const isDiscordWebhook = isDiscord && (c.spec.channel || '').startsWith('https://')
  const isDiscordBot = isDiscord && !isDiscordWebhook

  let endpointLabel: string
  if (isDiscordWebhook) endpointLabel = 'Webhook URL'
  else if (isDiscordBot) endpointLabel = 'Channel ID (optional)'
  else if (!usesChannel) endpointLabel = 'Endpoint URL'
  else if (c.spec.type === 'slack') endpointLabel = 'Webhook URL / channel'
  else if (c.spec.type === 'smtp') endpointLabel = 'Send to'
  else endpointLabel = 'Channel / chat ID'

  const endpointVal = usesChannel ? c.spec.channel || '' : c.spec.baseURL || ''
  const isOAuth = c.spec.auth === 'oauth'
  const secretField = isDiscordWebhook
    ? ''
    : isOAuth
      ? `<p class="agents-hint">This is an OAuth connection — use the ${ic('link')} button in the table to re-authorize. Client credentials aren’t edited here.</p>`
      : `<label>New ${isDiscordBot ? 'bot token' : 'secret / token'}<input name="secret" type="password" placeholder="leave blank to keep the current one" /><span class="agents-hint">Only set this to rotate the credential.</span></label>`
  const kindLabel = isDiscordWebhook ? 'Discord webhook' : isDiscordBot ? 'Discord chat' : ''
  return `<form class="agents-conn-form" data-editconn="${escapeHTML(c.metadata.name)}" data-usechannel="${usesChannel ? '1' : '0'}">
      <div class="agents-conn-formhead">
        <button type="button" class="agents-back" data-conncancel>${ic('arrow-left')} connections</button>
        <h4>Edit ${ic(CATEGORY_META[cat].icon)} <code>${escapeHTML(c.metadata.name)}</code>${kindLabel ? ` <span class="agents-badge">${escapeHTML(kindLabel)}</span>` : ''}</h4>
      </div>
      <label>Display name<input name="displayName" value="${escapeHTML(c.spec.displayName || '')}" placeholder="${escapeHTML(c.metadata.name)}" /></label>
      <label>${endpointLabel}<input name="endpoint" value="${escapeHTML(endpointVal)}" /></label>
      ${secretField}
      <div class="agents-form-actions"><button>Save changes</button><button type="button" class="secondary" data-conncancel>Cancel</button></div>
    </form>`
}

function renderConnForm(def: ConnTypeDef, oauthApps: Set<string>): string {
  const mode = connMode || def.modes?.[0].id || ''
  let fields = def.modes ? def.modes.find((m) => m.id === mode)!.fields : def.fields || []
  // Platform OAuth app configured (operator env)? Then OAuth modes need no
  // client id/secret — drop those fields.
  const isOAuthMode = fields.some((f) => f.key === 'clientID')
  let platformNote = ''
  if (isOAuthMode && oauthApps.has(def.id)) {
    fields = fields.filter((f) => f.key !== 'clientID' && f.key !== 'clientSecret')
    platformNote = `<div class="agents-platform-note">${ic('check')} Using the platform's ${escapeHTML(def.label)} OAuth app — no client id/secret needed. Create it, then click <strong>Connect</strong>.</div>`
  }
  return `
    <form class="agents-conn-form" data-type="${def.id}">
      <div class="agents-conn-formhead">
        <button type="button" class="agents-back" data-conntypes>${ic('arrow-left')} connection types</button>
        <h4>${ic(def.glyph)} ${escapeHTML(def.label)}</h4>
      </div>
      <p class="muted">${escapeHTML(def.desc)}</p>
      ${
        def.setup
          ? `<details class="agents-setup" open><summary>Before you start — setup steps</summary><ol>${def.setup.map((s) => `<li>${s}</li>`).join('')}</ol></details>`
          : ''
      }
      <label>Name *<input name="name" required pattern="[a-z0-9-]+" placeholder="my-${def.id}" /><span class="agents-hint">A short id you'll reference from agents.</span></label>
      ${
        def.modes
          ? `<div class="agents-modeseg">${def.modes.map((m) => `<button type="button" class="agents-modebtn ${m.id === mode ? 'sel' : ''}" data-connmode="${m.id}">${escapeHTML(m.label)}</button>`).join('')}</div>`
          : ''
      }
      ${platformNote}
      ${fields.map((f) => connFieldHTML(f)).join('')}
      ${def.advanced?.length ? `<details class="agents-adv"><summary>Advanced</summary>${def.advanced.map((f) => connFieldHTML(f)).join('')}</details>` : ''}
      <div><button>Create connection</button></div>
    </form>`
}

export function render(vc: ViewCtx): string {
  const conns = vc.store.connections
  const def = connType ? CONN_DEFS.find((d) => d.id === connType) : null
  const tile = (d: ConnTypeDef) => `<button class="agents-conn-tile" data-conntype="${d.id}">
               <span class="agents-conn-glyph">${ic(d.glyph)}</span>
               <span class="agents-conn-name">${escapeHTML(d.label)}</span>
               <span class="muted">${escapeHTML(d.desc)}</span>
             </button>`
  const groups = (['tool', 'channel', 'connection'] as ConnCategory[])
    .map((cat) => {
      const defs = CONN_DEFS.filter((d) => connCategory(d.id) === cat)
      if (!defs.length) return ''
      const m = CATEGORY_META[cat]
      return `<div class="agents-conn-group">
          <h5 class="agents-conn-grouphead">${ic(m.icon)} ${escapeHTML(m.label)}s <span class="muted">— ${escapeHTML(m.blurb)}</span></h5>
          <div class="agents-conn-types">${defs.map(tile).join('')}</div>
        </div>`
    })
    .join('')
  const editConn = connEdit ? conns.find((c) => c.metadata.name === connEdit) : undefined
  const adder = editConn
    ? renderConnEditForm(editConn)
    : def
      ? renderConnForm(def, vc.store.oauthApps)
      : `<div class="agents-conn-picker"><h4>Add a connection</h4>${groups}</div>`
  const catBadge = (id: string) => {
    const m = CATEGORY_META[connCategory(id)]
    return `<span class="agents-badge agents-badge-cat agents-cat-${connCategory(id)}">${ic(m.icon)} ${escapeHTML(m.label)}</span>`
  }
  const typeLabel = (c: Connection) => {
    if (c.spec.type !== 'discord') return c.spec.type
    return (c.spec.channel || '').startsWith('https://') ? 'discord webhook' : 'discord chat'
  }
  return `
    <div class="agents-panel">
      <h3>Connections</h3>
      <p class="muted">Shared credentials for external systems. Each is a ${ic('wrench')} <strong>Tool</strong> agents call, a ${ic('megaphone')} <strong>Channel</strong> they message you on, or a ${ic('plug')} generic <strong>Connection</strong>. Stored as Secrets in your workspace.</p>
      <table class="agents-table">
        <thead><tr><th>Name</th><th>Kind</th><th>Type</th><th>Endpoint / channel</th><th class="agents-th-actions">Actions</th></tr></thead>
        <tbody>
          ${
            conns.length
              ? conns
                  .map(
                    (c) => `<tr>
                      <td><span class="agents-cell-name">${escapeHTML(c.spec.displayName || c.metadata.name)}</span>${c.status?.webhookPath ? ` <span class="agents-inbound-on" title="Inbound enabled">${ic('swap')}</span>` : ''}${c.status?.oauthConnected ? ` <span class="agents-inbound-on" title="OAuth connected">${ic('link')}</span>` : ''}</td>
                      <td>${catBadge(c.spec.type)}</td>
                      <td><span class="agents-badge">${escapeHTML(typeLabel(c))}</span></td>
                      <td class="agents-cell-task muted">${escapeHTML(c.spec.baseURL || c.spec.channel || '—')}</td>
                      <td class="agents-row-actions">
                        <button class="agents-iconbtn" data-editconn="${escapeHTML(c.metadata.name)}" title="Edit">${ic('pencil')}</button>
                        ${connCategory(c.spec.type) === 'channel' ? `<button class="agents-iconbtn" data-testconn="${escapeHTML(c.metadata.name)}" title="Send a test message">${ic('send')}</button>` : ''}
                        ${connCategory(c.spec.type) === 'channel' ? `<button class="agents-iconbtn" data-inbound="${escapeHTML(c.metadata.name)}" title="${c.status?.webhookPath ? 'Inbound enabled' : 'Enable inbound chat'}">${ic('swap')}</button>` : ''}
                        ${c.spec.auth === 'oauth' ? `<button class="agents-iconbtn" data-oauth="${escapeHTML(c.metadata.name)}" title="${c.status?.oauthConnected ? 'Reconnect OAuth' : 'Connect OAuth'}">${ic('link')}</button>` : ''}
                        <button class="agents-iconbtn agents-iconbtn-danger" data-delconn="${escapeHTML(c.metadata.name)}" title="Delete">${ic('trash')}</button>
                      </td>
                    </tr>`,
                  )
                  .join('')
              : `<tr class="agents-empty-row"><td colspan="5"><span class="agents-empty">${ic('plug')} No connections yet — add one below.</span></td></tr>`
          }
        </tbody>
      </table>
      ${adder}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  root.querySelectorAll<HTMLElement>('[data-delconn]').forEach((el) =>
    el.addEventListener('click', async () => {
      if (await confirmModal({ title: `Delete connection “${el.dataset.delconn}”?`, danger: true, confirmLabel: 'Delete' })) void deleteConnection(vc, el.dataset.delconn!)
    }),
  )
  root.querySelectorAll<HTMLElement>('[data-oauth]').forEach((el) => el.addEventListener('click', () => void oauthConnect(vc, el.dataset.oauth!)))
  root.querySelectorAll<HTMLElement>('[data-testconn]').forEach((el) => el.addEventListener('click', () => void testConnection(vc, el.dataset.testconn!)))
  root.querySelectorAll<HTMLElement>('[data-inbound]').forEach((el) => el.addEventListener('click', () => void enableInbound(vc, el.dataset.inbound!)))
  // Edit an existing connection.
  root.querySelectorAll<HTMLElement>('[data-editconn]:not(form)').forEach((el) =>
    el.addEventListener('click', () => {
      connEdit = el.dataset.editconn!
      connType = null
      vc.rerender()
    }),
  )
  root.querySelectorAll<HTMLElement>('[data-conncancel]').forEach((el) =>
    el.addEventListener('click', () => {
      connEdit = null
      vc.rerender()
    }),
  )
  // Type picker → open that type's form.
  root.querySelectorAll<HTMLElement>('[data-conntype]').forEach((el) =>
    el.addEventListener('click', () => {
      connType = el.dataset.conntype!
      connMode = ''
      vc.rerender()
    }),
  )
  root.querySelector<HTMLElement>('[data-conntypes]')?.addEventListener('click', () => {
    connType = null
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-connmode]').forEach((el) =>
    el.addEventListener('click', () => {
      connMode = el.dataset.connmode!
      vc.rerender()
    }),
  )
  const f = root.querySelector<HTMLFormElement>('.agents-conn-form')
  if (!f) return
  // Edit-form submit (patch an existing connection).
  if (f.dataset.editconn) {
    f.addEventListener('submit', (e) => {
      e.preventDefault()
      const g = (n: string) => (f.querySelector<HTMLInputElement>(`[name=${n}]`)?.value || '').trim()
      const patch: Record<string, unknown> = { displayName: g('displayName') }
      if (f.dataset.usechannel === '1') patch.channel = g('endpoint')
      else patch.baseURL = g('endpoint')
      const secret = g('secret')
      if (secret) patch.secret = secret
      void updateConnection(vc, f.dataset.editconn!, patch).then((ok) => {
        if (ok) connEdit = null
      })
    })
    return
  }
  // Create-form submit.
  const def = connType ? CONN_DEFS.find((d) => d.id === connType) : null
  if (!def) return
  f.addEventListener('submit', (e) => {
    e.preventDefault()
    const v: Record<string, string> = {}
    f.querySelectorAll<HTMLInputElement>('input[name]').forEach((el) => (v[el.name] = el.value.trim()))
    const mode = connMode || def.modes?.[0].id || ''
    const body = def.build(v, mode)
    void createConnection(vc, body).then((ok) => {
      if (ok) {
        connType = null
        connMode = ''
      }
    })
  })
}
