// Agent Wiring tab: a form-based counterpart to the Flow canvas. Everything the
// flow tab wires by dragging cables, this tab edits as plain forms scoped to one
// agent — model, schedules, triggers, channels (in + out), and tools/toolsets.
// The schedule/trigger sections reuse the menu views with an agentRef filter so
// there's a single source of truth for those forms; channels and tools are
// agent-link operations written here.

import { ic } from '../portalkit/icons'
import type { ViewCtx } from '../view'
import type { Agent, Connection } from '../types'
import { escapeHTML, effectiveChannels } from '../types'
import { connCategory } from '../conn-defs'
import { updateAgent, linkToolset, wireToolTo, testConnection, enableInbound } from '../actions'
import * as schedules from './schedules'
import * as triggers from './triggers'

// Chat-capable channels: telegram/slack (webhook inbound) and the Discord bot
// (channel is not a https:// webhook URL). Send-only otherwise. An agent
// receives on every channel it lists, so any bound channel is a receiver.
function channelInbound(c: { spec: { type: string; channel?: string }; status?: { webhookPath?: string } }) {
  const isDiscordWebhook = c.spec.type === 'discord' && (c.spec.channel || '').startsWith('https://')
  const isDiscordBot = c.spec.type === 'discord' && !isDiscordWebhook
  const canReceive = c.spec.type === 'telegram' || c.spec.type === 'slack' || isDiscordBot
  if (!canReceive) return { on: false, canEnable: false, note: 'Send-only — this channel can notify you, but can’t receive chat.' }
  if (isDiscordBot) return { on: true, canEnable: false, note: 'Inbound is automatic — the Discord bot delivers messages while linked.' }
  if (c.status?.webhookPath) return { on: true, canEnable: false, note: 'Receiving — messages from this channel reach the agent.' }
  return { on: false, canEnable: true, note: 'Not receiving yet — enable inbound to register the webhook.' }
}

// chanConnOptions renders the channel-Connection <option>s for one editor row.
function chanConnOptions(channels: Connection[], selected: string): string {
  return (
    `<option value="">— pick a connection —</option>` +
    channels.map((c) => `<option value="${escapeHTML(c.metadata.name)}" ${c.metadata.name === selected ? 'selected' : ''}>${escapeHTML(c.spec.displayName || c.metadata.name)} (${escapeHTML(c.spec.type)})</option>`).join('')
  )
}

// chanRowHTML is one editable channel row: role name, backing connection, and a
// primary radio (native radio-group exclusivity keeps exactly one primary).
function chanRowHTML(channels: Connection[], ch: { name?: string; connectionRef?: string; primary?: boolean }): string {
  return `<div class="agents-chan-row" data-chan-row>
      <input class="agents-chan-name" placeholder="primary" value="${escapeHTML(ch.name || '')}" />
      <select class="agents-chan-conn">${chanConnOptions(channels, ch.connectionRef || '')}</select>
      <label class="agents-chan-primary" title="Default channel for output with no channel set"><input type="radio" name="chan-primary" ${ch.primary ? 'checked' : ''} /> primary</label>
      <button type="button" class="agents-iconbtn agents-iconbtn-danger" data-chan-del title="Remove channel">${ic('trash')}</button>
    </div>`
}

export function render(vc: ViewCtx, a: Agent): string {
  const name = a.metadata.name

  // Model
  const model = a.spec?.models?.chat || ''
  const modelOptions =
    `<option value="">— no model —</option>` +
    vc.store.credentials.map((c) => `<option value="${escapeHTML(c.name)}" ${c.name === model ? 'selected' : ''}>${escapeHTML(c.name)}${c.model ? ` (${escapeHTML(c.model)})` : ''}</option>`).join('')
  const modelSec = `<div class="agents-panel">
      <h3>${ic('brain')} Model</h3>
      <p class="muted">Which credential this agent reasons with. Personas and budget live in ${ic('settings')} Settings.</p>
      <label>Model credential<select data-wire-model>${modelOptions}</select>
        ${vc.store.credentials.length === 0 ? `<span class="muted" style="font-size:12px">No models yet — add one under ${ic('cpu')} Models.</span>` : ''}
      </label>
    </div>`

  // Channels (in + out) — an agent can bind several. The primary one is the
  // default notify target; schedules and triggers can route to any by name.
  const channels = vc.store.connections.filter((c) => connCategory(c.spec.type) === 'channel')
  const bound = effectiveChannels(a)
  const editorRows = (bound.length ? bound : []).map((ch) => chanRowHTML(channels, ch)).join('')
  // Per-bound-channel inbound status + enable/test, keyed by connection.
  const inboundRows = bound
    .map((ch) => {
      const conn = channels.find((c) => c.metadata.name === ch.connectionRef)
      if (!conn) return `<div class="agents-inbound-line"><span class="agents-badge">${escapeHTML(ch.name)}</span><span class="muted">Connection “${escapeHTML(ch.connectionRef)}” not found — pick one above and save.</span></div>`
      const inb = channelInbound(conn)
      return `<div class="agents-inbound-line">
          <span class="agents-badge ${inb.on ? 'agents-cat-channel' : ''}">${escapeHTML(ch.name)}${ch.primary ? ' ★' : ''} · ${ic('swap')} inbound ${inb.on ? 'on' : 'off'}</span>
          <span class="muted">${escapeHTML(inb.note)}</span>
          <span class="agents-inbound-actions">
            ${inb.canEnable ? `<button type="button" class="secondary" data-wire-inbound="${escapeHTML(ch.connectionRef)}">Enable inbound</button>` : ''}
            <button type="button" class="secondary" data-wire-test="${escapeHTML(ch.connectionRef)}">${ic('send')} Test</button>
          </span>
        </div>`
    })
    .join('')
  const chanSec = `<div class="agents-panel">
      <h3>${ic('megaphone')} Channels</h3>
      <p class="muted">Where this agent messages you — and, for chat channels, where you message it. Bind a <strong>primary</strong> channel plus any secondaries (e.g. a dedicated incidents or news channel); schedules and triggers can route to any of them. Manage channel credentials under ${ic('plug')} Connections.</p>
      ${channels.length === 0 ? `<p class="agents-hint">No channels yet — add one under ${ic('plug')} Connections (Telegram, Slack, Discord, email).</p>` : ''}
      <div class="agents-chan-editor" data-chan-editor>${editorRows}</div>
      <div class="agents-form-actions">
        <button type="button" class="secondary" data-chan-add ${channels.length === 0 ? 'disabled' : ''}>${ic('plus')} Add channel</button>
        <button type="button" data-chan-save>${ic('check')} Save channels</button>
      </div>
      ${inboundRows ? `<div class="agents-chan-inbound">${inboundRows}</div>` : ''}
    </div>`

  // Tools & toolsets
  const agentToolsets = new Set([...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])])
  const agentTools = new Set([...(a.spec?.tools?.interactive?.connections || []), ...(a.spec?.tools?.background?.connections || [])])
  const toolConns = vc.store.connections.filter((c) => connCategory(c.spec.type) === 'tool')
  const toolsetRows = vc.store.toolsets.length
    ? vc.store.toolsets
        .map(
          (t) =>
            `<label class="agents-check"><input type="checkbox" data-wire-toolset="${escapeHTML(t.metadata.name)}" ${agentToolsets.has(t.metadata.name) ? 'checked' : ''} /> ${escapeHTML(t.spec.displayName || t.metadata.name)}</label>`,
        )
        .join('')
    : `<p class="agents-hint">No toolsets yet — create one under ${ic('puzzle')} Toolsets.</p>`
  const toolRows = toolConns.length
    ? toolConns
        .map(
          (c) =>
            `<label class="agents-check"><input type="checkbox" data-wire-tool="${escapeHTML(c.metadata.name)}" ${agentTools.has(c.metadata.name) ? 'checked' : ''} /> ${escapeHTML(c.spec.displayName || c.metadata.name)} <span class="muted">${escapeHTML(c.spec.type)}</span></label>`,
        )
        .join('')
    : `<p class="agents-hint">No tools yet — add one under ${ic('plug')} Connections.</p>`
  const toolsSec = `<div class="agents-panel">
      <h3>${ic('wrench')} Tools &amp; toolsets</h3>
      <p class="muted">What this agent can call. Toolsets are shared bundles; direct tools grant a single connection.</p>
      <fieldset class="agents-wire-fs"><legend>${ic('puzzle')} Toolsets</legend>${toolsetRows}</fieldset>
      <fieldset class="agents-wire-fs"><legend>${ic('wrench')} Direct tools</legend>${toolRows}</fieldset>
    </div>`

  return `<div class="agents-wiring">
      ${modelSec}
      <div class="agents-wire-sec" data-sec="sched">${schedules.render(vc, name)}</div>
      <div class="agents-wire-sec" data-sec="trig">${triggers.render(vc, name)}</div>
      ${chanSec}
      ${toolsSec}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement, a: Agent): void {
  const name = a.metadata.name

  // Model
  root.querySelector<HTMLSelectElement>('[data-wire-model]')?.addEventListener('change', (e) => {
    void updateAgent(vc, name, { modelCredential: (e.target as HTMLSelectElement).value }, 'Model updated.')
  })

  // Schedules / Triggers reuse the menu views, scoped to this agent. Wire each
  // within its own container so their forms/buttons don't cross-fire.
  const schedRoot = root.querySelector<HTMLElement>('[data-sec="sched"]')
  if (schedRoot) schedules.wire(vc, schedRoot, name)
  const trigRoot = root.querySelector<HTMLElement>('[data-sec="trig"]')
  if (trigRoot) triggers.wire(vc, trigRoot, name)

  // Channels: a multi-row editor. Add appends a blank row client-side (no
  // rerender, so in-progress input survives); Save collects all rows and
  // replaces the agent's channel list in one call.
  const channelConns = vc.store.connections.filter((c) => connCategory(c.spec.type) === 'channel')
  const editor = root.querySelector<HTMLElement>('[data-chan-editor]')
  root.querySelector<HTMLElement>('[data-chan-add]')?.addEventListener('click', () => {
    if (!editor) return
    const firstPrimary = !editor.querySelector('.agents-chan-row input[type=radio]:checked')
    editor.insertAdjacentHTML('beforeend', chanRowHTML(channelConns, { primary: firstPrimary }))
  })
  editor?.addEventListener('click', (e) => {
    const del = (e.target as HTMLElement).closest('[data-chan-del]')
    if (del) del.closest('[data-chan-row]')?.remove()
  })
  root.querySelector<HTMLElement>('[data-chan-save]')?.addEventListener('click', () => {
    const rows = [...(editor?.querySelectorAll<HTMLElement>('[data-chan-row]') || [])]
    const chans = rows
      .map((r) => ({
        name: (r.querySelector<HTMLInputElement>('.agents-chan-name')?.value || '').trim(),
        connectionRef: (r.querySelector<HTMLSelectElement>('.agents-chan-conn')?.value || '').trim(),
        primary: !!r.querySelector<HTMLInputElement>('input[type=radio]')?.checked,
      }))
      .filter((c) => c.name && c.connectionRef)
    const seen = new Set<string>()
    for (const c of chans) {
      if (seen.has(c.name)) {
        vc.notify(`Duplicate channel name “${c.name}” — names must be unique.`)
        return
      }
      seen.add(c.name)
    }
    void updateAgent(vc, name, { channels: chans }, 'Channels saved.')
  })
  root.querySelectorAll<HTMLElement>('[data-wire-inbound]').forEach((el) =>
    el.addEventListener('click', () => void enableInbound(vc, el.dataset.wireInbound!)),
  )
  root.querySelectorAll<HTMLElement>('[data-wire-test]').forEach((el) =>
    el.addEventListener('click', () => void testConnection(vc, el.dataset.wireTest!)),
  )

  // Toolsets: link (checked) / unlink (unchecked) for this agent only.
  root.querySelectorAll<HTMLInputElement>('[data-wire-toolset]').forEach((el) =>
    el.addEventListener('change', () => {
      const ts = el.dataset.wireToolset!
      if (el.checked) {
        void linkToolset(vc, name, ts).then(() => vc.notify('Toolset linked.'))
      } else {
        const cur = vc.store.agent(name)
        const inter = (cur?.spec?.tools?.interactive?.toolsets || []).filter((t) => t !== ts)
        const bg = (cur?.spec?.tools?.background?.toolsets || []).filter((t) => t !== ts)
        void updateAgent(vc, name, { interactiveToolsets: inter, backgroundToolsets: bg }, 'Toolset unlinked.')
      }
    }),
  )

  // Direct tools: grant (checked) / revoke (unchecked). Families are re-derived
  // from the resulting connection set so the grant stays consistent.
  root.querySelectorAll<HTMLInputElement>('[data-wire-tool]').forEach((el) =>
    el.addEventListener('change', () => {
      const cn = el.dataset.wireTool!
      if (el.checked) {
        void wireToolTo(vc, name, 'agent', cn)
      } else {
        const cur = vc.store.agent(name)
        const inter = (cur?.spec?.tools?.interactive?.connections || []).filter((x) => x !== cn)
        const bg = (cur?.spec?.tools?.background?.connections || []).filter((x) => x !== cn)
        void updateAgent(
          vc,
          name,
          { interactiveConnections: inter, backgroundConnections: bg, interactiveFamilies: vc.store.familiesFor(inter), backgroundFamilies: vc.store.familiesFor(bg) },
          'Tool removed.',
        )
      }
    }),
  )
}
