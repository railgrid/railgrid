// Agent chat tab — the default view on an agent's detail page. Streams replies
// over SSE, shows tool-call rows inline, and persists the open session per agent
// (per tenant) so a refresh reopens the same thread. Chat state is module-level:
// there is one element instance for the app's lifetime, and keeping it here (not
// in the store) means menu re-renders never disturb a live transcript.

import type { ViewCtx } from '../view'
import type { Agent, ChatMessage, SessionMeta } from '../types'
import { escapeHTML, sessionLabel } from '../types'

let messages: ChatMessage[] = []
let sessions: SessionMeta[] = []
let sessionID = ''
let chatAgent = '' // agent whose sessions + messages are currently loaded
let streaming = false
let error: string | null = null

// resetForTenant clears chat state on a workspace switch so a thread never leaks
// across tenants.
export function resetForTenant(): void {
  messages = []
  sessions = []
  sessionID = ''
  chatAgent = ''
  streaming = false
  error = null
}

// ---- session persistence ---------------------------------------------------

function sessKey(vc: ViewCtx, agent: string): string {
  const t = vc.api.tenant()
  return `kedge:agents:session:${t.orgUUID || ''}:${t.workspaceUUID || ''}:${agent}`
}
function lastSession(vc: ViewCtx, agent: string): string {
  try {
    return localStorage.getItem(sessKey(vc, agent)) || ''
  } catch {
    return ''
  }
}
function rememberSession(vc: ViewCtx, agent: string, id: string): void {
  try {
    localStorage.setItem(sessKey(vc, agent), id)
  } catch {
    /* storage disabled — session just won't survive refresh */
  }
}
function newSessionID(): string {
  try {
    return crypto.randomUUID()
  } catch {
    return 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8)
  }
}

// ensureChat loads an agent's sessions + active thread the first time its chat
// tab renders (guarded by chatAgent so repeated renders during streaming don't
// refetch or clobber the live transcript).
async function ensureChat(vc: ViewCtx, name: string): Promise<void> {
  if (chatAgent === name) return
  chatAgent = name
  sessions = []
  messages = []
  sessionID = lastSession(vc, name)
  await loadSessions(vc, name)
  if (!sessionID || (sessions.length && !sessions.some((s) => s.id === sessionID))) {
    sessionID = sessions[0]?.id || newSessionID()
  }
  if (!sessionID) sessionID = newSessionID()
  rememberSession(vc, name, sessionID)
  await loadMessages(vc, name, sessionID)
}

async function loadSessions(vc: ViewCtx, name: string): Promise<void> {
  try {
    sessions = await vc.api.list<SessionMeta>(`/api/agents/${encodeURIComponent(name)}/sessions`)
  } catch {
    sessions = []
  }
}

// loadMessages hydrates the transcript from the store. The backend returns
// newest-first; reverse to chronological. Skipped mid-stream so a late fetch
// can't wipe the reply being typed.
async function loadMessages(vc: ViewCtx, name: string, session: string): Promise<void> {
  if (streaming) return
  try {
    const items = await vc.api.list<{ role: string; content: string }>(`/api/agents/${encodeURIComponent(name)}/messages?session=${encodeURIComponent(session)}`)
    messages = items
      .slice()
      .reverse()
      .map((m) => ({ role: m.role === 'assistant' ? 'assistant' : m.role === 'tool' ? 'tool' : 'user', content: m.content }))
  } catch {
    /* leave the current transcript in place on error */
  }
  vc.rerender()
}

function switchSession(vc: ViewCtx, id: string): void {
  if (!id || id === sessionID || streaming) return
  sessionID = id
  rememberSession(vc, chatAgent, id)
  messages = []
  error = null
  vc.rerender()
  void loadMessages(vc, chatAgent, id)
}

function newChat(vc: ViewCtx, root: HTMLElement): void {
  if (streaming) return
  sessionID = newSessionID()
  rememberSession(vc, chatAgent, sessionID)
  messages = []
  error = null
  vc.rerender()
  root.querySelector<HTMLInputElement>('.agents-chat-form input')?.focus()
}

async function chat(vc: ViewCtx, agent: string, text: string): Promise<void> {
  if (streaming) return
  if (!sessionID) {
    sessionID = newSessionID()
    rememberSession(vc, agent, sessionID)
  }
  messages.push({ role: 'user', content: text })
  const assistant: ChatMessage = { role: 'assistant', content: '' }
  messages.push(assistant)
  streaming = true
  error = null
  vc.rerender()
  try {
    for await (const ev of vc.api.chatStream(agent, text, sessionID)) {
      if (ev.event === 'delta' && ev.data?.text) {
        assistant.content += ev.data.text
        vc.rerender()
      } else if (ev.event === 'tool' && ev.data?.name) {
        const row: ChatMessage = { role: 'tool', error: !!ev.data.error, content: `${ev.data.name}(${ev.data.args || ''}) → ${ev.data.result || ''}` }
        messages.splice(messages.length - 1, 0, row)
        vc.rerender()
      } else if (ev.event === 'error') {
        error = ev.data?.message || 'stream error'
      }
    }
  } catch (e) {
    const msg = (e as Error).message
    error = /not found|credentials|kedge-agents-model|not configured/i.test(msg) ? 'No model configured — assign one in Settings.' : 'Chat failed: ' + msg
  }
  streaming = false
  vc.rerender()
  // A first turn creates the session server-side; refresh the picker so it shows
  // up (with its preview) without disturbing the live transcript.
  if (chatAgent) void loadSessions(vc, chatAgent).then(() => vc.rerender())
}

// ---- render / wire ---------------------------------------------------------

export function render(a: Agent): string {
  if (!a.spec?.models?.chat) {
    return `<div class="agents-empty"><p class="muted">No model assigned. Open <strong>Settings</strong> and pick a model credential to start chatting.</p></div>`
  }
  const list = sessions.slice()
  if (sessionID && !list.some((s) => s.id === sessionID)) {
    list.unshift({ id: sessionID, preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' })
  }
  const picker = list.map((s) => `<option value="${escapeHTML(s.id)}" ${s.id === sessionID ? 'selected' : ''}>${escapeHTML(sessionLabel(s))}</option>`).join('')
  return `
    <div class="agents-chat">
      <div class="agents-chat-head">
        <select class="agents-session-picker" ${streaming ? 'disabled' : ''} title="Chat sessions">${picker}</select>
        <button type="button" class="agents-newchat secondary" ${streaming ? 'disabled' : ''}>＋ New chat</button>
      </div>
      ${error ? `<div class="agents-err">${escapeHTML(error)}</div>` : ''}
      <div class="agents-log">
        ${
          messages.length
            ? messages
                .map((m) =>
                  m.role === 'tool'
                    ? `<div class="agents-msg tool ${m.error ? 'err' : ''}"><div class="agents-toolrow">🔧 ${escapeHTML(m.content)}</div></div>`
                    : `<div class="agents-msg ${m.role}"><div class="agents-role">${m.role}</div><div class="agents-body">${escapeHTML(m.content) || (streaming && m.role === 'assistant' ? '…' : '')}</div></div>`,
                )
                .join('')
            : `<p class="muted">No messages yet. Say hi.</p>`
        }
      </div>
      <form class="agents-chat-form"><input placeholder="Message ${escapeHTML(a.metadata.name)}…" ${streaming ? 'disabled' : ''} autocomplete="off" /><button ${streaming ? 'disabled' : ''}>${streaming ? '…' : 'Send'}</button></form>
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement, a: Agent): void {
  void ensureChat(vc, a.metadata.name)
  const picker = root.querySelector<HTMLSelectElement>('.agents-session-picker')
  picker?.addEventListener('change', () => switchSession(vc, picker.value))
  root.querySelector<HTMLButtonElement>('.agents-newchat')?.addEventListener('click', () => newChat(vc, root))
  const form = root.querySelector<HTMLFormElement>('.agents-chat-form')
  form?.addEventListener('submit', (e) => {
    e.preventDefault()
    const input = form.querySelector<HTMLInputElement>('input')!
    const t = input.value.trim()
    if (t) {
      input.value = ''
      void chat(vc, a.metadata.name, t)
    }
  })
  const log = root.querySelector<HTMLElement>('.agents-log')
  if (log) log.scrollTop = log.scrollHeight
}
