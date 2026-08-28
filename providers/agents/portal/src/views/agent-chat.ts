// The chat playground — always live against the agent's current config.
//
// Streaming is incremental: deltas accumulate in a buffer flushed once per
// animation frame, and the flush replaces only the streaming message object, so
// lit updates a single <agents-chat-message> child rather than the page. The
// log auto-scrolls ONLY when the user was already at the bottom; scrolling up
// to read while a reply streams now works.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState } from '../ui/states'
import { toast } from '../ui/toast'
import { confirmModal } from '../portalkit/modal'
import { sessionLabel, type ChatMessage, type RunSummary, type SessionMeta, type ToolCall, type TranscriptMessage } from '../types'
import type { ServerEvent } from '../store'

import './chat-message'

// Phases that mean "still working", for spotting a run this view is not attached
// to. PendingApproval counts: it is waiting on the user, not finished.
const LIVE_RUN_PHASES = new Set(['Pending', 'Running', 'PendingApproval'])

interface StartData {
  runID: string
  sessionID: string
}
interface DeltaData {
  text: string
}
interface ToolStartData {
  id: string
  name: string
  args?: string
}
interface ToolEndData extends ToolStartData {
  result?: string
  error?: string
  durationMS?: number
}
interface ApprovalData {
  runID: string
  inboxID: string
  tool: string
  args: string
  content?: string
}
interface DoneData {
  runID: string
  content: string
  usage?: { inputTokens: number; outputTokens: number; usdMicros: number }
}
interface ErrorData {
  runID?: string
  message: string
}

export class AgentChat extends StoreElement {
  @property({ type: String }) name = ''

  @state() private messages: ChatMessage[] = []
  @state() private sessions: SessionMeta[] = []
  @state() private sessionID = ''
  @state() private streaming = false
  @state() private loadError: string | null = null
  @state() private draft = ''
  // A run in this session that is still working without us watching it — the
  // stream was closed (tab shut, navigation) but the run outlived it. Surfaced so
  // the answer does not look lost, and so the user does not re-ask and pay twice.
  @state() private orphanRun: RunSummary | null = null

  private loadedFor = ''
  private abort: AbortController | null = null
  private liveRunID = ''
  private streamingID = ''
  private deltaBuffer = ''
  private flushHandle = 0
  private atBottom = true
  private msgSeq = 0

  connectedCallback(): void {
    super.connectedCallback()
    this.store?.addEventListener('server', this.onServerEvent as EventListener)
  }

  disconnectedCallback(): void {
    this.store?.removeEventListener('server', this.onServerEvent as EventListener)
    this.abort?.abort()
    super.disconnectedCallback()
  }

  protected willUpdate(): void {
    super.willUpdate()
    if (this.name && this.loadedFor !== this.name) {
      this.loadedFor = this.name
      // The handoff check waits for openAgent: it resets the transcript and
      // picks the session, which would wipe an already-sent turn.
      void this.openAgent(this.name).then(() => this.maybeAutoSend())
      return
    }
    // Off the update cycle: maybeAutoSend starts a turn, and mutating reactive
    // state from inside willUpdate re-enters the cycle so updateComplete never
    // resolves.
    queueMicrotask(() => this.maybeAutoSend())
  }

  // maybeAutoSend claims a prompt another view handed off (Connections' assisted
  // setup) and sends it as this agent's next turn. store.takePendingPrompt
  // CONSUMES the handoff, so this fires exactly once: a re-render, a tab switch
  // back to Chat, or a second element instance all find it already taken, and a
  // page refresh drops it with the store.
  private maybeAutoSend(): void {
    if (!this.store || this.streaming) return
    const text = this.store.takePendingPrompt(this.name)
    if (!text) return
    // A handed-off task gets its own thread rather than being appended to
    // whatever conversation happened to be open.
    this.sessionID = this.newSessionID()
    this.remember(this.sessionID)
    this.messages = []
    this.draft = text
    void this.send()
  }

  // A resumed run (approval granted server-side) finishes out of band, so
  // reload the transcript when its run reaches a terminal phase.
  private onServerEvent = (e: Event): void => {
    const ev = (e as CustomEvent<ServerEvent>).detail
    if (ev.type !== 'run' || !ev.data.id) return
    // Either the run this view started (resumed out of band after an approval) or
    // one it merely found still working — both land their reply in the transcript.
    const watched = ev.data.id === this.liveRunID || ev.data.id === this.orphanRun?.id
    if (!watched) return
    if (ev.data.phase === 'Succeeded' || ev.data.phase === 'Failed' || ev.data.phase === 'Aborted') {
      if (ev.data.id === this.liveRunID) this.liveRunID = ''
      if (ev.data.id === this.orphanRun?.id) this.orphanRun = null
      void this.loadMessages(this.sessionID)
    }
  }

  // ---- session plumbing ----------------------------------------------------

  private sessKey(agent: string): string {
    const t = this.api.tenant()
    return `faros:agents:session:${t.orgUUID || ''}:${t.workspaceUUID || ''}:${agent}`
  }

  private newSessionID(): string {
    try {
      return crypto.randomUUID()
    } catch {
      return 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8)
    }
  }

  private remember(id: string): void {
    try {
      localStorage.setItem(this.sessKey(this.name), id)
    } catch {
      /* storage disabled — the session just won't survive a refresh */
    }
  }

  private async openAgent(name: string): Promise<void> {
    this.messages = []
    this.sessions = []
    this.loadError = null
    let want = ''
    try {
      want = localStorage.getItem(this.sessKey(name)) || ''
    } catch {
      want = ''
    }
    await this.loadSessions()
    if (!want || (this.sessions.length && !this.sessions.some((s) => s.id === want))) {
      want = this.sessions[0]?.id || this.newSessionID()
    }
    this.sessionID = want
    this.remember(want)
    await this.loadMessages(want)
  }

  private async loadSessions(): Promise<void> {
    try {
      this.sessions = await this.api.listSessions(this.name)
    } catch {
      this.sessions = []
    }
  }

  // loadMessages hydrates the transcript. The backend returns newest-first;
  // reverse to chronological. Skipped mid-stream so a late fetch can't wipe the
  // reply being typed.
  private async loadMessages(session: string): Promise<void> {
    if (this.streaming || !session) return
    try {
      const items = await this.api.listMessages(this.name, session)
      this.messages = rebuildTranscript(items.slice().reverse())
      this.loadError = null
      this.atBottom = true
    } catch (e) {
      this.loadError = (e as Error).message
    }
    void this.findOrphanRun(session)
  }

  // cancelOrphan stops a run this view is not attached to. The run registry only
  // cancels on the replica executing it; elsewhere the API stamps it Aborted, so
  // either way the banner clears.
  private async cancelOrphan(): Promise<void> {
    const run = this.orphanRun
    if (!run) return
    try {
      await this.api.cancelRun(run.id)
      this.orphanRun = null
      toast('ok', 'Stopping the run…')
    } catch (e) {
      toast('error', `Could not stop it: ${(e as Error).message}`)
    }
  }

  // findOrphanRun looks for a run still executing in this session. Runs outlive
  // the stream that started them, so one can be mid-flight with nobody attached;
  // without this the transcript just looks like the reply never came.
  private async findOrphanRun(session: string): Promise<void> {
    if (this.streaming) {
      this.orphanRun = null
      return
    }
    try {
      const page = await this.api.listRuns({ agent: this.name, session, limit: 5 })
      this.orphanRun = page.items.find((r) => LIVE_RUN_PHASES.has(r.phase)) ?? null
    } catch {
      // Best-effort: a failed lookup must not break the transcript view.
      this.orphanRun = null
    }
  }

  private switchSession(id: string): void {
    if (!id || id === this.sessionID || this.streaming) return
    this.sessionID = id
    this.remember(id)
    this.messages = []
    void this.loadMessages(id)
  }

  private newChat(): void {
    if (this.streaming) return
    this.sessionID = this.newSessionID()
    this.remember(this.sessionID)
    this.messages = []
    void this.updateComplete.then(() => this.querySelector<HTMLTextAreaElement>('.agents-composer textarea')?.focus())
  }

  private async deleteSession(): Promise<void> {
    const id = this.sessionID
    if (!id || this.streaming) return
    const authority = this.captureAuthority()
    const agent = this.name
    const ok = await confirmModal({
      title: 'Delete this chat?',
      message: 'The transcript is removed from the agent’s memory for this session.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !this.authorityIsCurrent(authority)) return
    try {
      await authority.api.deleteSession(agent, id)
      if (!this.authorityIsCurrent(authority)) return
      toast('ok', 'Chat deleted.')
      this.sessions = this.sessions.filter((s) => s.id !== id)
      this.messages = []
      this.sessionID = this.sessions[0]?.id || this.newSessionID()
      this.remember(this.sessionID)
      await this.loadMessages(this.sessionID)
    } catch (e) {
      toast('error', `Delete failed: ${(e as Error).message}`)
    }
  }

  // ---- streaming -----------------------------------------------------------

  private patchMessage(id: string, patch: Partial<ChatMessage>): void {
    this.messages = this.messages.map((m) => (m.id === id ? { ...m, ...patch } : m))
  }

  private current(): ChatMessage | undefined {
    return this.messages.find((m) => m.id === this.streamingID)
  }

  // Deltas are batched to one flush per frame: a fast model emits tokens far
  // quicker than the browser can lay out markdown.
  private queueDelta(text: string): void {
    this.deltaBuffer += text
    if (this.flushHandle) return
    this.flushHandle = requestAnimationFrame(() => {
      this.flushHandle = 0
      const buf = this.deltaBuffer
      this.deltaBuffer = ''
      const cur = this.current()
      if (!cur || !buf) return
      this.patchMessage(cur.id, { content: cur.content + buf })
    })
  }

  private flushNow(): void {
    if (this.flushHandle) {
      cancelAnimationFrame(this.flushHandle)
      this.flushHandle = 0
    }
    const buf = this.deltaBuffer
    this.deltaBuffer = ''
    const cur = this.current()
    if (cur && buf) this.patchMessage(cur.id, { content: cur.content + buf })
  }

  private setTool(id: string, patch: Partial<ToolCall>, create?: ToolCall): void {
    const cur = this.current()
    if (!cur) return
    const existing = cur.tools.find((t) => t.id === id)
    const tools = existing ? cur.tools.map((t) => (t.id === id ? { ...t, ...patch } : t)) : create ? [...cur.tools, create] : cur.tools
    this.patchMessage(cur.id, { tools })
  }

  private async send(): Promise<void> {
    const text = this.draft.trim()
    if (!text || this.streaming) return
    if (!this.sessionID) {
      this.sessionID = this.newSessionID()
      this.remember(this.sessionID)
    }
    this.draft = ''
    const userID = `u${++this.msgSeq}`
    const assistantID = `a${++this.msgSeq}`
    this.streamingID = assistantID
    this.messages = [
      ...this.messages,
      { id: userID, role: 'user', content: text, tools: [] },
      { id: assistantID, role: 'assistant', content: '', tools: [], streaming: true },
    ]
    this.streaming = true
    this.atBottom = true
    const ac = new AbortController()
    this.abort = ac

    try {
      for await (const ev of this.api.chatStream(this.name, text, this.sessionID, ac.signal)) {
        switch (ev.event) {
          case 'start': {
            const d = ev.data as StartData
            this.liveRunID = d.runID
            this.patchMessage(assistantID, { runID: d.runID })
            if (d.sessionID) this.sessionID = d.sessionID
            break
          }
          case 'delta':
            this.queueDelta((ev.data as DeltaData).text || '')
            break
          case 'tool_start': {
            const d = ev.data as ToolStartData
            this.flushNow()
            this.setTool(d.id, {}, { id: d.id, name: d.name, args: d.args, pending: true })
            break
          }
          case 'tool_end': {
            const d = ev.data as ToolEndData
            this.setTool(d.id, { result: d.result, error: d.error, durationMS: d.durationMS, pending: false }, {
              id: d.id,
              name: d.name,
              args: d.args,
              result: d.result,
              error: d.error,
              durationMS: d.durationMS,
              pending: false,
            })
            break
          }
          case 'approval_required': {
            const d = ev.data as ApprovalData
            this.flushNow()
            const cur = this.current()
            this.patchMessage(assistantID, {
              content: d.content || cur?.content || '',
              approval: { runID: d.runID, inboxID: d.inboxID, tool: d.tool, args: d.args },
            })
            // The run stays paused server-side; watch /api/events for the resume.
            this.liveRunID = d.runID
            void this.store.load('inbox')
            break
          }
          case 'done': {
            const d = ev.data as DoneData
            this.flushNow()
            const cur = this.current()
            this.patchMessage(assistantID, { content: d.content || cur?.content || '', usage: d.usage })
            this.liveRunID = ''
            break
          }
          case 'error': {
            const d = ev.data as ErrorData
            this.flushNow()
            this.patchMessage(assistantID, { error: d.message || 'stream error' })
            this.liveRunID = ''
            break
          }
        }
      }
    } catch (e) {
      this.flushNow()
      if ((e as Error).name === 'AbortError') this.patchMessage(assistantID, { error: 'Stopped.' })
      else this.patchMessage(assistantID, { error: `Chat failed: ${(e as Error).message}` })
    }
    this.flushNow()
    this.streaming = false
    this.streamingID = ''
    this.abort = null
    this.patchMessage(assistantID, { streaming: false })
    // A first turn creates the session server-side; refresh the picker so it
    // appears (with its preview) without disturbing the transcript.
    void this.loadSessions().then(() => this.requestUpdate())
  }

  // stop drops the stream client-side AND cancels the run server-side, so an
  // expensive tool loop doesn't keep burning budget after the user gave up.
  private async stop(): Promise<void> {
    const runID = this.liveRunID
    this.abort?.abort()
    if (!runID) return
    try {
      await this.api.cancelRun(runID)
    } catch (e) {
      toast('error', `Cancel failed: ${(e as Error).message}`)
    }
  }

  private async resolveApproval(inboxID: string, decision: 'approve' | 'deny'): Promise<void> {
    const target = this.messages.find((m) => m.approval?.inboxID === inboxID)
    try {
      await this.api.resolveInbox(inboxID, decision)
      if (target?.approval) this.patchMessage(target.id, { approval: { ...target.approval, resolved: decision } })
      toast('ok', decision === 'approve' ? 'Approved — resuming the run.' : 'Denied.')
      void this.store.load('inbox')
    } catch (e) {
      toast('error', `Could not ${decision}: ${(e as Error).message}`)
    }
  }

  // ---- scroll --------------------------------------------------------------

  private onScroll(e: Event): void {
    const el = e.target as HTMLElement
    this.atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48
  }

  protected updated(): void {
    if (!this.atBottom) return
    const log = this.querySelector<HTMLElement>('.agents-log')
    if (log) log.scrollTop = log.scrollHeight
  }

  // ---- render --------------------------------------------------------------

  render(): TemplateResult {
    const a = this.store.agent(this.name)
    const hasModel = !!a?.spec?.models?.chat
    const list = this.sessions.slice()
    if (this.sessionID && !list.some((s) => s.id === this.sessionID)) {
      list.unshift({ id: this.sessionID, preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' })
    }
    return html`<div class="agents-chat" @agents-approval=${(e: CustomEvent<{ inboxID: string; decision: 'approve' | 'deny' }>) => void this.resolveApproval(e.detail.inboxID, e.detail.decision)}>
      <div class="agents-chat-head">
        <select class="k-input agents-session-picker"
          aria-label="Chat session"
          ?disabled=${this.streaming}
          @change=${(e: Event) => this.switchSession((e.target as HTMLSelectElement).value)}
        >
          ${list.map((s) => html`<option value=${s.id} ?selected=${s.id === this.sessionID}>${sessionLabel(s)}</option>`)}
        </select>
        <button class="k-btn k-btn--ghost agents-iconbtn" aria-label="New chat" title="New chat" ?disabled=${this.streaming} @click=${() => this.newChat()}>
          ${icon('plus')}
        </button>
        <button
          class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger"
          aria-label="Delete this chat"
          title="Delete this chat"
          ?disabled=${this.streaming}
          @click=${() => void this.deleteSession()}
        >
          ${icon('trash')}
        </button>
      </div>
      ${hasModel
        ? nothing
        : html`<div class="agents-warn-banner">
            No model assigned — pick a model credential in the Config pane to start chatting.
          </div>`}
      ${this.orphanRun && !this.streaming
        ? html`<div class="agents-orphan-banner" role="status">
            <span>${icon('clock')}</span>
            <span class="agents-orphan-text">
              This chat has a run still working — it kept going after the stream closed. Its reply will appear here when it finishes.
            </span>
            <button
              class="k-btn k-btn--ghost agents-linkbtn"
              @click=${() => this.navigate({ kind: 'run', id: this.orphanRun!.id })}
            >
              View progress
            </button>
            <button class="k-btn k-btn--ghost secondary" @click=${() => void this.cancelOrphan()}>Stop it</button>
          </div>`
        : nothing}
      ${this.loadError ? errorState(this.loadError, () => void this.loadMessages(this.sessionID)) : nothing}
      <div class="agents-log" aria-live="polite" aria-busy=${this.streaming ? 'true' : 'false'} @scroll=${(e: Event) => this.onScroll(e)}>
        ${this.messages.length
          ? repeat(
              this.messages,
              (m) => m.id,
              (m) => html`<agents-chat-message .message=${m}></agents-chat-message>`,
            )
          : html`<p class="muted">No messages yet. Say hi.</p>`}
      </div>
      <form
        class="agents-composer"
        @submit=${(e: Event) => {
          e.preventDefault()
          void this.send()
        }}
      >
        <textarea class="k-input"
          rows="1"
          placeholder=${`Message ${this.name}…  (Enter to send, Shift+Enter for a newline)`}
          .value=${this.draft}
          ?disabled=${!hasModel}
          @input=${(e: Event) => {
            const t = e.target as HTMLTextAreaElement
            this.draft = t.value
            // Grow with the content up to a cap; the log keeps the rest.
            t.style.height = 'auto'
            t.style.height = `${Math.min(t.scrollHeight, 180)}px`
          }}
          @keydown=${(e: KeyboardEvent) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              void this.send()
            }
          }}
        ></textarea>
        ${this.streaming
          ? html`<button type="button" class="k-btn k-btn--ghost secondary agents-stop" @click=${() => void this.stop()}>${icon('pause')} Stop</button>`
          : html`<button class="k-btn k-btn--primary" type="submit" ?disabled=${!hasModel || !this.draft.trim()}>${icon('send')} Send</button>`}
      </form>
    </div>`
  }
}

// rebuildTranscript turns the flat persisted message list into the same shape
// the live stream produces: tool turns (role "tool", call metadata alongside
// the result in `content`) become tool cards on the assistant turn that follows
// them, so reopening a session shows exactly what it showed while streaming.
// `metadata.args` arrives already redacted.
export function rebuildTranscript(chronological: TranscriptMessage[]): ChatMessage[] {
  const out: ChatMessage[] = []
  let pending: ToolCall[] = []
  const flush = (into?: ChatMessage): void => {
    if (!pending.length) return
    if (into) into.tools = pending
    // Tool calls with no assistant turn after them (an aborted or still-paused
    // run) would otherwise vanish — keep them on a contentless turn.
    else out.push({ id: `orphan-${pending[0].id}`, role: 'assistant', content: '', tools: pending })
    pending = []
  }
  for (const m of chronological) {
    if (m.role === 'tool') {
      const md = m.metadata || {}
      pending.push({
        id: `m${m.id}`,
        name: md.tool || 'tool',
        args: md.args,
        result: m.content,
        error: md.error,
        durationMS: md.durationMS,
        pending: false,
      })
      continue
    }
    if (m.role !== 'user' && m.role !== 'assistant') continue // system prompts aren't shown
    if (m.role === 'user') flush()
    const msg: ChatMessage = { id: `m${m.id}`, role: m.role, content: m.content, tools: [], runID: m.runID }
    if (m.role === 'assistant') flush(msg)
    out.push(msg)
  }
  flush()
  return out
}

if (!customElements.get('agents-agent-chat')) customElements.define('agents-agent-chat', AgentChat)
