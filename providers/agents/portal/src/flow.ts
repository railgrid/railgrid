// FlowCanvas — the "Patchbay" node-graph view of a single agent. An imperative,
// self-managing component: it owns its own pan/zoom/drag state and DOM so it can
// survive the portal's full-innerHTML re-render. The host (element.ts) keeps a
// reference to the instance, re-attaches root after each render, and calls
// update() with a freshly-derived model. The canvas emits intents through
// callbacks; it never talks to the API itself — the mapping from a dragged cable
// to a real spec mutation lives with the data owner.

export type FNodeType =
  | 'agent'
  | 'schedule'
  | 'trigger'
  | 'chat'
  | 'connection'
  | 'model'
  | 'tools'
  | 'tool'
  | 'toolset'
  | 'output'
  | 'delegate'

export type FStatus = 'ok' | 'warn' | 'off'

export interface FieldSpec {
  key: string
  label: string
  kind: 'text' | 'textarea' | 'select' | 'chips' | 'static'
  value?: string
  options?: { value: string; label: string }[]
  chips?: { value: string; label: string; on: boolean }[]
  mono?: boolean
  hint?: string
  placeholder?: string
}

export interface FNode {
  id: string
  type: FNodeType
  title: string
  sub?: string
  tags?: string[]
  status?: [FStatus, string]
  ins: string[]
  outs: string[]
  core?: boolean
  fields?: FieldSpec[]
  canRun?: boolean
  canDelete?: boolean
  draft?: boolean // unsaved: shows a Create button instead of auto-save
  createKey?: string // palette key used to create this draft
}

// DraftSpec describes a new, unsaved node dropped from the palette: the fields
// to collect before it can be written, and how it wires to the agent.
export interface DraftSpec {
  title: string
  nodeType: FNodeType // how the draft node renders (icon/color)
  ins: string[]
  outs: string[]
  fields: FieldSpec[]
  outPort?: string // this node's out-port …
  agentPort?: string // … wired to this agent in-port (schedule/trigger/model)
}

export interface FWire {
  from: [string, string] // [nodeId, portName]
  to: [string, string]
}

export interface FlowModel {
  key: string // stable per-agent key for position persistence
  nodes: FNode[]
  wires: FWire[]
  palette: PaletteGroup[] // left-rail contents, derived from live data by the host
}

export interface FlowCallbacks {
  onEdit(nodeId: string, values: Record<string, string | string[]>): void
  onLink(from: [string, string], to: [string, string]): void
  onRun(nodeId: string): void
  onDelete(nodeId: string): void
  onOpenChat(): void
  onToast(msg: string): void
  // draftFor returns the create-form spec for a "new:<key>" palette entry, or
  // null for keys that aren't standalone objects (chat/tools/notify/delegate).
  draftFor(key: string): DraftSpec | null
  // create writes the object from the draft's values; returns the real node id
  // (e.g. "sched:daily") on success so the canvas can place it, or null on fail.
  create(key: string, values: Record<string, string | string[]>): Promise<string | null>
  // addExisting references an already-existing object (dragged from the palette)
  // to this agent — patches the reference and returns the real node id for
  // placement, or null on failure/toast.
  addExisting(id: string): Promise<string | null>
}

// A palette entry is either an existing object (id = its real node id, e.g.
// "sched:daily" / "toolset:ops" / "conn:tg") or a create action (id =
// "new:<draftKey>"). linked entries are already referenced by this agent and
// render dimmed + inert.
export interface PaletteEntry {
  id: string
  label: string
  icon: FNodeType
  linked?: boolean
  sub?: string
}
export interface PaletteGroup {
  label: string
  entries: PaletteEntry[]
}
const NEW_PREFIX = 'new:'

interface TypeDef {
  label: string
  color: string // css var name
  icon: string // inner svg
}

const TYPES: Record<FNodeType, TypeDef> = {
  schedule: { label: 'Schedule', color: '--flow-schedule', icon: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>' },
  trigger: { label: 'Trigger', color: '--flow-trigger', icon: '<path d="M13 2 4.5 13.5H11l-1 8.5L20 9.5h-6.5z"/>' },
  chat: { label: 'Chat', color: '--flow-chat', icon: '<path d="M4 5h16v10.5H10l-4.5 3.5v-3.5H4z"/>' },
  connection: { label: 'Connection', color: '--flow-connection', icon: '<path d="M9 2.5v6M15 2.5v6M7 8.5h10v2.5a5 5 0 0 1-10 0zM12 16v5.5"/>' },
  agent: { label: 'Agent', color: '--flow-agent', icon: '<path d="M12 2.5 3.5 7v10L12 21.5 20.5 17V7z"/><circle cx="12" cy="12" r="3.2"/>' },
  model: { label: 'Model', color: '--flow-model', icon: '<rect x="5.5" y="5.5" width="13" height="13" rx="2.5"/><path d="M9 2.5v3M15 2.5v3M9 18.5v3M15 18.5v3M2.5 9h3M2.5 15h3M18.5 9h3M18.5 15h3"/>' },
  tools: { label: 'Tools', color: '--flow-tools', icon: '<path d="M14.5 6.5a3.8 3.8 0 0 1-5 5l-5.5 5.5 2.5 2.5 5.5-5.5a3.8 3.8 0 0 0 5-5l-2.3 2.3-2.2-.5-.5-2.2z"/>' },
  tool: { label: 'Tool', color: '--flow-tool', icon: '<path d="M14.5 6.5a3.8 3.8 0 0 1-5 5l-5.5 5.5 2.5 2.5 5.5-5.5a3.8 3.8 0 0 0 5-5l-2.3 2.3-2.2-.5-.5-2.2z"/>' },
  toolset: { label: 'Toolset', color: '--flow-toolset', icon: '<path d="M12 2.5 3 7l9 4.5L21 7z"/><path d="M3 12l9 4.5L21 12M3 16.5 12 21l9-4.5"/>' },
  output: { label: 'Notify', color: '--flow-output', icon: '<path d="M6 16.5V11a6 6 0 1 1 12 0v5.5l1.8 1.8H4.2z"/><path d="M10 20.3a2 2 0 0 0 4 0"/>' },
  delegate: { label: 'Delegate', color: '--flow-delegate', icon: '<circle cx="6" cy="6" r="2.2"/><circle cx="6" cy="18" r="2.2"/><circle cx="17.5" cy="9" r="2.2"/><path d="M6 8.2v7.6M6 12h5.5a4 4 0 0 0 4-4"/>' },
}

const NS = 'http://www.w3.org/2000/svg'
const NW = 224
const PORT0 = 60
const PORTGAP = 24
const esc = (s: string): string => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] as string)
const svgIcon = (t: FNodeType): string =>
  `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round" stroke-linecap="round">${TYPES[t].icon}</svg>`
const cvar = (t: FNodeType): string => `var(${TYPES[t].color})`

interface Pos {
  x: number
  y: number
}

export class FlowCanvas {
  private cb: FlowCallbacks
  private model: FlowModel = { key: '', nodes: [], wires: [], palette: [] }
  private railEl!: HTMLElement
  private world!: HTMLElement
  private svg!: SVGSVGElement
  private canvas!: HTMLElement
  private modal!: HTMLElement
  private dialog!: HTMLElement
  private toastEl!: HTMLElement
  private nodeEls = new Map<string, HTMLElement>()
  private pos = new Map<string, Pos>()
  private view = { x: 60, y: 20, k: 0.85 }
  private sel: string | null = null
  private editing: string | null = null // node whose edit dialog is open
  private saveT = 0
  private loadedKey = ''
  private needFit = false
  private hiWire = -1
  private linking: { from: [string, string]; el: SVGPathElement } | null = null
  private drafts: FNode[] = [] // unsaved nodes dropped from the palette
  private draftWires: FWire[] = []
  private draftSeq = 0

  // Live model plus any in-flight drafts — everything the canvas renders.
  private nodes(): FNode[] {
    return this.drafts.length ? [...this.model.nodes, ...this.drafts] : this.model.nodes
  }
  private wires(): FWire[] {
    return this.draftWires.length ? [...this.model.wires, ...this.draftWires] : this.model.wires
  }
  private node(id: string): FNode | undefined {
    return this.nodes().find((n) => n.id === id)
  }

  constructor(root: HTMLElement, cb: FlowCallbacks) {
    this.cb = cb
    root.classList.add('flow-root')
    root.innerHTML = `
      <div class="flow-rail"></div>
      <div class="flow-canvas">
        <div class="flow-world"><svg class="flow-wires" width="4000" height="2600"></svg></div>
      </div>
      <div class="flow-zoom">
        <button data-z="out">−</button><span class="flow-zval mono">100%</span><button data-z="in">+</button><button data-z="fit" title="Fit to view">⤢</button>
      </div>
      <div class="flow-legend"></div>
      <div class="flow-modal hidden"><div class="flow-modal-bg"></div><div class="flow-dialog" role="dialog" aria-modal="true"></div></div>
      <div class="flow-toast"></div>`
    this.canvas = root.querySelector('.flow-canvas') as HTMLElement
    this.world = root.querySelector('.flow-world') as HTMLElement
    this.svg = root.querySelector('.flow-wires') as SVGSVGElement
    this.modal = root.querySelector('.flow-modal') as HTMLElement
    this.dialog = root.querySelector('.flow-dialog') as HTMLElement
    ;(this.modal.querySelector('.flow-modal-bg') as HTMLElement).onclick = () => this.closeEditor()
    this.toastEl = root.querySelector('.flow-toast') as HTMLElement
    this.railEl = root.querySelector('.flow-rail') as HTMLElement
    this.buildRail()
    this.buildLegend(root.querySelector('.flow-legend') as HTMLElement)
    this.wireZoom(root)
    this.wirePan()
    this.applyView()
  }

  // ---- public: refresh from live data --------------------------------------
  update(model: FlowModel): void {
    if (model.key !== this.loadedKey) {
      this.loadedKey = model.key
      this.pos.clear()
      this.sel = null
      // Fresh agent with no saved arrangement → auto-fit so every node is on
      // screen; a saved layout (returned true) restores the user's own view.
      this.needFit = !this.loadPositions(model.key)
    }
    this.model = model
    this.buildRail() // palette is derived from live data, so rebuild each update
    this.layout()
    this.syncNodes()
    this.renderWires()
    // A per-field auto-save reloads data and re-renders; keep the dialog open
    // (and refreshed) if the edited node still exists.
    if (this.editing && this.nodes().some((n) => n.id === this.editing)) this.renderModal()
    else if (this.editing) this.closeEditor()
    this.applyView()
    if (this.needFit && this.nodes().length) {
      // Defer the initial fit one frame: called synchronously right after the
      // host is attached, getBoundingClientRect() can still report a stale size.
      this.needFit = false
      requestAnimationFrame(() => this.fit())
    }
  }

  toast(msg: string): void {
    this.toastEl.textContent = msg
    this.toastEl.classList.add('show')
    window.clearTimeout((this.toastEl as unknown as { _t?: number })._t)
    ;(this.toastEl as unknown as { _t?: number })._t = window.setTimeout(() => this.toastEl.classList.remove('show'), 2200)
  }

  // ---- layout ---------------------------------------------------------------
  private layout(): void {
    // Five signal-flow columns, left → right: connections feed sources
    // (schedules/triggers/chat), the brain (model/tools) sits just left of the
    // agent, and outputs (notify/delegates) sit to its right.
    const colX: Record<string, number> = { connection: 20, source: 320, brain: 620, agent: 900, out: 1220 }
    const cursor: Record<string, number> = { connection: 210, source: 20, brain: 120, agent: 300, out: 170 }
    const colOf = (t: FNodeType): keyof typeof colX =>
      t === 'agent'
        ? 'agent'
        : t === 'connection' || t === 'tool'
          ? 'connection'
          : t === 'output' || t === 'delegate'
            ? 'out'
            : t === 'model' || t === 'tools' || t === 'toolset'
              ? 'brain'
              : 'source'
    const step = (n: FNode): number => Math.max(140, PORT0 + Math.max(n.ins.length, n.outs.length) * PORTGAP + 58)
    // The portal loads data incrementally (agents → schedules → triggers → …),
    // so layout() runs several times as nodes arrive. Seed each column's cursor
    // below the lowest node already placed in it, or a later-arriving node would
    // land on top of an earlier one.
    for (const n of this.nodes()) {
      if (!this.pos.has(n.id)) continue
      const col = colOf(n.type)
      cursor[col] = Math.max(cursor[col], (this.pos.get(n.id) as Pos).y + step(n))
    }
    for (const n of this.nodes()) {
      if (this.pos.has(n.id)) continue
      const col = colOf(n.type)
      const y = cursor[col]
      cursor[col] = y + step(n)
      this.pos.set(n.id, { x: colX[col], y })
    }
  }

  private nodeH(n: FNode): number {
    return PORT0 + Math.max(0, Math.max(n.ins.length, n.outs.length) - 1) * PORTGAP + 52
  }

  // ---- nodes ----------------------------------------------------------------
  private syncNodes(): void {
    const ids = new Set(this.nodes().map((n) => n.id))
    for (const [id, el] of this.nodeEls) {
      if (!ids.has(id)) {
        el.remove()
        this.nodeEls.delete(id)
      }
    }
    for (const n of this.nodes()) this.renderNode(n)
  }

  private renderNode(n: FNode): void {
    let el = this.nodeEls.get(n.id)
    const fresh = !el
    if (!el) {
      el = document.createElement('div')
      el.className = 'flow-node' + (n.core ? ' core' : '')
      el.dataset.id = n.id
      this.world.appendChild(el)
      this.nodeEls.set(n.id, el)
    }
    el.style.setProperty('--nc', cvar(n.type))
    el.style.setProperty('--nc-soft', `color-mix(in srgb, ${cvar(n.type)} 15%, var(--color-surface-raised, #fff))`)
    el.style.setProperty('--nc-line', `color-mix(in srgb, ${cvar(n.type)} 40%, var(--color-border-default, #ccc))`)
    el.classList.toggle('sel', this.sel === n.id)
    el.classList.toggle('draft', !!n.draft)
    const tags = (n.tags || []).map((t) => `<span class="flow-tag acc">${esc(t)}</span>`).join('')
    const st = n.status ? `<div class="flow-statusline"><span class="flow-led ${n.status[0]}"></span>${esc(n.status[1])}</div>` : ''
    const editable = (n.fields || []).some((f) => f.kind !== 'static')
    el.innerHTML = `
      <div class="flow-nhead">
        <span class="flow-nic">${svgIcon(n.type)}</span>
        <span class="flow-nmeta"><span class="flow-ntype">${TYPES[n.type].label}</span><span class="flow-ntitle">${esc(n.title)}</span></span>
        ${editable ? '<button class="flow-editbtn" title="Edit" aria-label="Edit">✎</button>' : ''}
      </div>
      <div class="flow-nbody">
        ${n.sub ? `<p class="flow-nsub">${n.sub}</p>` : ''}
        ${tags ? `<div class="flow-tagrow">${tags}</div>` : ''}
        ${st}
      </div>`
    n.ins.forEach((p, i) => el!.appendChild(this.mkPort(n, p, 'in', i)))
    n.outs.forEach((p, i) => el!.appendChild(this.mkPort(n, p, 'out', i)))
    const p = this.pos.get(n.id) as Pos
    el.style.left = p.x + 'px'
    el.style.top = p.y + 'px'
    const head = el.querySelector('.flow-nhead') as HTMLElement
    head.onpointerdown = (e) => this.startDragNode(e, n)
    const editBtn = el.querySelector('.flow-editbtn') as HTMLElement | null
    if (editBtn) {
      // Pointerdown-stop keeps the header-drag from swallowing the click.
      editBtn.onpointerdown = (e) => e.stopPropagation()
      editBtn.onclick = (e) => {
        e.stopPropagation()
        this.openEditor(n.id)
      }
    }
    el.onpointerdown = (e) => {
      if ((e.target as HTMLElement).classList.contains('flow-port')) return
      this.select(n.id)
    }
    el.ondblclick = (e) => {
      if ((e.target as HTMLElement).classList.contains('flow-port')) return
      this.openEditor(n.id)
    }
    if (fresh) {
      el.classList.add('enter')
      window.setTimeout(() => el && el.classList.remove('enter'), 260)
    }
  }

  private mkPort(n: FNode, name: string, side: 'in' | 'out', i: number): HTMLElement {
    const p = document.createElement('span')
    p.className = `flow-port io-${side}`
    p.style.top = PORT0 + i * PORTGAP + 'px'
    p.style.setProperty('--pc', cvar(n.type))
    p.dataset.label = name
    p.dataset.port = name
    p.dataset.side = side
    p.dataset.node = n.id
    p.onpointerdown = (e) => this.startLink(e, n, name, side)
    return p
  }

  // ---- ports & positions ----------------------------------------------------
  private portPos(nodeId: string, port: string, side: 'in' | 'out'): Pos {
    const n = this.nodes().find((x) => x.id === nodeId)
    const p = this.pos.get(nodeId)
    if (!n || !p) return { x: 0, y: 0 }
    const list = side === 'out' ? n.outs : n.ins
    const idx = Math.max(0, list.indexOf(port))
    return { x: p.x + (side === 'out' ? NW + (n.core ? 14 : 0) : 0), y: p.y + PORT0 + idx * PORTGAP }
  }

  // ---- wires ----------------------------------------------------------------
  private path(a: Pos, b: Pos): string {
    const dx = Math.max(46, Math.abs(b.x - a.x) * 0.5)
    return `M ${a.x} ${a.y} C ${a.x + dx} ${a.y}, ${b.x - dx} ${b.y}, ${b.x} ${b.y}`
  }

  private renderWires(): void {
    // keep the live-linking temp path if present
    this.svg.innerHTML = ''
    this.wires().forEach((w, i) => {
      const a = this.portPos(w.from[0], w.from[1], 'out')
      const b = this.portPos(w.to[0], w.to[1], 'in')
      const src = this.nodes().find((n) => n.id === w.from[0])
      const col = src ? cvar(src.type) : 'var(--color-border-strong)'
      const d = this.path(a, b)
      const hit = document.createElementNS(NS, 'path')
      hit.setAttribute('class', 'flow-wire-hit')
      hit.setAttribute('d', d)
      hit.addEventListener('pointerenter', () => this.hi(i, true))
      hit.addEventListener('pointerleave', () => this.hi(i, false))
      this.svg.appendChild(hit)
      const path = document.createElementNS(NS, 'path')
      path.setAttribute('class', 'flow-wire flow' + (i === this.hiWire ? ' hi' : ''))
      path.setAttribute('d', d)
      path.setAttribute('stroke', col)
      path.style.color = col
      this.svg.appendChild(path)
    })
    if (this.linking) this.svg.appendChild(this.linking.el)
  }

  private hi(i: number, on: boolean): void {
    this.hiWire = on ? i : -1
    this.renderWires()
  }

  // ---- selection ------------------------------------------------------------
  private select(id: string): void {
    this.sel = id
    for (const [k, el] of this.nodeEls) el.classList.toggle('sel', k === id)
  }
  private deselect(): void {
    this.sel = null
    for (const el of this.nodeEls.values()) el.classList.remove('sel')
  }

  // ---- edit dialog (centered, auto-saving) ----------------------------------
  private onKey = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') this.closeEditor()
  }
  private openEditor(id: string): void {
    this.select(id)
    this.editing = id
    this.renderModal()
    this.modal.classList.remove('hidden')
    window.addEventListener('keydown', this.onKey)
    // focus the first editable control for keyboard users
    const first = this.dialog.querySelector<HTMLElement>('input, textarea, select')
    if (first) first.focus()
  }
  private closeEditor(): void {
    this.editing = null
    this.modal.classList.add('hidden')
    window.removeEventListener('keydown', this.onKey)
  }

  private renderModal(): void {
    const n = this.nodes().find((x) => x.id === this.editing)
    if (!n) return this.closeEditor()
    this.dialog.style.setProperty('--nc', cvar(n.type))
    this.dialog.style.setProperty('--nc-soft', `color-mix(in srgb, ${cvar(n.type)} 15%, var(--color-surface-raised, #fff))`)
    this.dialog.style.setProperty('--nc-line', `color-mix(in srgb, ${cvar(n.type)} 40%, var(--color-border-default, #ccc))`)
    const cables = this.wires()
      .filter((w) => w.from[0] === n.id || w.to[0] === n.id)
      .map((w) => {
        const out = w.from[0] === n.id
        const other = this.nodes().find((x) => x.id === (out ? w.to[0] : w.from[0]))
        const src = this.nodes().find((x) => x.id === w.from[0])
        const col = src ? cvar(src.type) : 'var(--color-border-strong)'
        return `<div class="flow-wireitem"><span class="sw" style="background:${col}"></span><span class="dir">${out ? 'OUT →' : '← IN'}</span> ${esc(other?.title || '')}</div>`
      })
      .join('') || `<div class="flow-wireitem muted">no cables yet — drag from a port on the canvas</div>`
    const fields = (n.fields || []).map((f) => this.fieldHTML(f)).join('')
    const editable = (n.fields || []).some((f) => f.kind !== 'static')
    const draft = !!n.draft
    this.dialog.innerHTML = `
      <div class="flow-dialog-head">
        <span class="flow-nic">${svgIcon(n.type)}</span>
        <span class="t"><span class="flow-ntype">${draft ? 'New ' + TYPES[n.type].label.toLowerCase() : TYPES[n.type].label}</span><div class="flow-ntitle">${esc(n.title)}</div></span>
        <span class="flow-saved" data-saved>saved ✓</span>
        <button class="flow-x" data-x aria-label="Close">✕</button>
      </div>
      <div class="flow-dialog-body">
        ${fields || '<p class="flow-nsub">Nothing to edit here — this node is wired from the canvas.</p>'}
        ${draft ? '' : `<div class="flow-field"><label>Cables</label><div class="flow-wirelist">${cables}</div></div>`}
      </div>
      <div class="flow-dialog-foot">
        ${draft ? '<span class="flow-autonote">Create writes it as a real object</span>' : editable ? '<span class="flow-autonote">Changes save automatically</span>' : '<span></span>'}
        <div class="flow-dialog-actions">
          ${draft ? '<button class="flow-btn" data-discard>Discard</button><button class="flow-btn primary" data-create>Create</button>' : ''}
          ${!draft && n.type === 'chat' ? '<button class="flow-btn primary" data-chat>Open chat</button>' : ''}
          ${!draft && n.canRun ? '<button class="flow-btn" data-run>▶ Test</button>' : ''}
          ${!draft && n.canDelete ? '<button class="flow-btn danger" data-del>Remove</button>' : ''}
        </div>
      </div>`
    ;(this.dialog.querySelector('[data-x]') as HTMLElement).onclick = () => (draft ? this.discardEditing() : this.closeEditor())
    const chatBtn = this.dialog.querySelector('[data-chat]') as HTMLElement | null
    if (chatBtn) chatBtn.onclick = () => this.cb.onOpenChat()
    const runBtn = this.dialog.querySelector('[data-run]') as HTMLElement | null
    if (runBtn) runBtn.onclick = () => this.cb.onRun(n.id)
    const delBtn = this.dialog.querySelector('[data-del]') as HTMLElement | null
    if (delBtn)
      delBtn.onclick = () => {
        this.closeEditor()
        this.cb.onDelete(n.id)
      }
    const discardBtn = this.dialog.querySelector('[data-discard]') as HTMLElement | null
    if (discardBtn) discardBtn.onclick = () => this.discardEditing()
    const createBtn = this.dialog.querySelector('[data-create]') as HTMLElement | null
    if (createBtn) createBtn.onclick = () => void this.doCreate(n)
    if (!draft) {
      // auto-save: commit on blur/change of any control, and on chip toggle
      this.dialog.querySelectorAll<HTMLElement>('input, textarea, select').forEach((el) => {
        el.addEventListener('change', () => this.autosave())
      })
      this.dialog.querySelectorAll<HTMLElement>('.flow-chip').forEach((c) => {
        c.onclick = () => {
          c.classList.toggle('on')
          this.autosave()
        }
      })
    } else {
      // draft: live-mirror the title from the name field for a bit of feedback
      const nameEl = this.dialog.querySelector<HTMLInputElement>('[data-k="name"]')
      const titleEl = this.dialog.querySelector<HTMLElement>('.flow-ntitle')
      if (nameEl && titleEl) nameEl.addEventListener('input', () => (titleEl.textContent = nameEl.value || TYPES[n.type].label))
      this.dialog.querySelectorAll<HTMLElement>('.flow-chip').forEach((c) => {
        c.onclick = () => c.classList.toggle('on')
      })
    }
  }

  private discardEditing(): void {
    const id = this.editing
    this.closeEditor()
    if (id) this.discardDraft(id)
  }

  private async doCreate(n: FNode): Promise<void> {
    const values: Record<string, string | string[]> = {}
    this.dialog.querySelectorAll<HTMLElement>('[data-k]').forEach((el) => {
      const k = el.dataset.k as string
      if (el.classList.contains('flow-chiprow')) values[k] = Array.from(el.querySelectorAll<HTMLElement>('.flow-chip.on')).map((c) => c.dataset.v as string)
      else values[k] = (el as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement).value
    })
    // No universal name check here — some create forms (tools) may reuse an
    // existing object instead of naming a new one. cb.create() validates and
    // returns null (keeping the dialog open) when input is insufficient.
    const btn = this.dialog.querySelector<HTMLButtonElement>('[data-create]')
    if (btn) {
      btn.disabled = true
      btn.textContent = 'Creating…'
    }
    const draftId = n.id
    const p = this.pos.get(draftId)
    const realId = await this.cb.create(n.createKey || n.type, values)
    if (!realId) {
      if (btn) {
        btn.disabled = false
        btn.textContent = 'Create'
      }
      return // create() surfaced its own error
    }
    // create() reloaded data → the real node already rendered at an auto-layout
    // spot. Drop the draft and move the real node to where the draft sat.
    this.editing = null
    this.modal.classList.add('hidden')
    window.removeEventListener('keydown', this.onKey)
    this.discardDraft(draftId)
    if (p) {
      this.pos.set(realId, p)
      const el = this.nodeEls.get(realId)
      if (el) {
        el.style.left = p.x + 'px'
        el.style.top = p.y + 'px'
      }
      this.renderWires()
      this.savePositions()
    }
  }

  // Debounced commit of all fields in the open dialog. onEdit is partial-safe,
  // so only the keys present here are patched.
  private autosave(): void {
    window.clearTimeout(this.saveT)
    this.saveT = window.setTimeout(() => {
      const n = this.nodes().find((x) => x.id === this.editing)
      if (!n) return
      this.commit(n)
      const badge = this.dialog.querySelector<HTMLElement>('[data-saved]')
      if (badge) {
        badge.classList.add('show')
        window.setTimeout(() => badge.classList.remove('show'), 1600)
      }
    }, 350)
  }

  private fieldHTML(f: FieldSpec): string {
    const mono = f.mono ? ' mono' : ''
    let ctl = ''
    if (f.kind === 'text') ctl = `<input class="${mono}" data-k="${esc(f.key)}" value="${esc(f.value || '')}" placeholder="${esc(f.placeholder || '')}">`
    else if (f.kind === 'textarea') ctl = `<textarea class="${mono}" data-k="${esc(f.key)}" placeholder="${esc(f.placeholder || '')}">${esc(f.value || '')}</textarea>`
    else if (f.kind === 'select')
      ctl = `<select data-k="${esc(f.key)}">${(f.options || [])
        .map((o) => `<option value="${esc(o.value)}" ${o.value === f.value ? 'selected' : ''}>${esc(o.label)}</option>`)
        .join('')}</select>`
    else if (f.kind === 'chips')
      ctl = `<div class="flow-chiprow" data-k="${esc(f.key)}">${(f.chips || [])
        .map((c) => `<span class="flow-chip ${c.on ? 'on' : ''}" data-v="${esc(c.value)}">${esc(c.label)}</span>`)
        .join('')}</div>`
    else ctl = `<div class="flow-static${mono}">${esc(f.value || '—')}</div>`
    return `<div class="flow-field"><label>${esc(f.label)}</label>${ctl}${f.hint ? `<span class="hint">${esc(f.hint)}</span>` : ''}</div>`
  }

  private commit(n: FNode): void {
    const values: Record<string, string | string[]> = {}
    this.dialog.querySelectorAll<HTMLElement>('[data-k]').forEach((el) => {
      const k = el.dataset.k as string
      if (el.classList.contains('flow-chiprow')) {
        values[k] = Array.from(el.querySelectorAll<HTMLElement>('.flow-chip.on')).map((c) => c.dataset.v as string)
      } else {
        values[k] = (el as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement).value
      }
    })
    this.cb.onEdit(n.id, values)
  }

  // ---- drag node ------------------------------------------------------------
  private startDragNode(e: PointerEvent, n: FNode): void {
    e.stopPropagation()
    this.select(n.id)
    const el = this.nodeEls.get(n.id) as HTMLElement
    el.setPointerCapture(e.pointerId)
    el.classList.add('dragging')
    const start = this.pos.get(n.id) as Pos
    const sx = e.clientX
    const sy = e.clientY
    const ox = start.x
    const oy = start.y
    const move = (ev: PointerEvent): void => {
      this.pos.set(n.id, { x: ox + (ev.clientX - sx) / this.view.k, y: oy + (ev.clientY - sy) / this.view.k })
      const p = this.pos.get(n.id) as Pos
      el.style.left = p.x + 'px'
      el.style.top = p.y + 'px'
      this.renderWires()
    }
    const up = (): void => {
      el.releasePointerCapture(e.pointerId)
      el.classList.remove('dragging')
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      this.savePositions()
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  // ---- drag-to-connect ------------------------------------------------------
  private startLink(e: PointerEvent, n: FNode, port: string, side: 'in' | 'out'): void {
    e.stopPropagation()
    e.preventDefault()
    const from: [string, string] = [n.id, port]
    const temp = document.createElementNS(NS, 'path')
    temp.setAttribute('class', 'flow-wire linking')
    temp.setAttribute('stroke', cvar(n.type))
    temp.style.color = cvar(n.type)
    this.linking = { from, el: temp }
    this.svg.appendChild(temp)
    const anchor = this.portPos(n.id, port, side)
    const toWorld = (ev: PointerEvent): Pos => {
      const r = this.canvas.getBoundingClientRect()
      return { x: (ev.clientX - r.left - this.view.x) / this.view.k, y: (ev.clientY - r.top - this.view.y) / this.view.k }
    }
    const move = (ev: PointerEvent): void => {
      const cur = toWorld(ev)
      temp.setAttribute('d', side === 'out' ? this.path(anchor, cur) : this.path(cur, anchor))
      const tgt = document.elementFromPoint(ev.clientX, ev.clientY) as HTMLElement | null
      this.world.querySelectorAll('.flow-port.drop').forEach((p) => p.classList.remove('drop'))
      if (tgt && tgt.classList.contains('flow-port') && tgt.dataset.side !== side && tgt.dataset.node !== n.id) tgt.classList.add('drop')
    }
    const up = (ev: PointerEvent): void => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      this.world.querySelectorAll('.flow-port.drop').forEach((p) => p.classList.remove('drop'))
      const tgt = document.elementFromPoint(ev.clientX, ev.clientY) as HTMLElement | null
      this.linking = null
      temp.remove()
      if (tgt && tgt.classList.contains('flow-port') && tgt.dataset.side !== side && tgt.dataset.node !== n.id) {
        const outEnd: [string, string] = side === 'out' ? from : [tgt.dataset.node as string, tgt.dataset.port as string]
        const inEnd: [string, string] = side === 'out' ? [tgt.dataset.node as string, tgt.dataset.port as string] : from
        this.cb.onLink(outEnd, inEnd)
      }
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  // ---- pan / zoom -----------------------------------------------------------
  private wirePan(): void {
    this.canvas.addEventListener('pointerdown', (e) => {
      const t = e.target as HTMLElement
      if (t.closest('.flow-node') || t.classList.contains('flow-wire-hit') || t.classList.contains('flow-port')) return
      this.deselect()
      this.canvas.classList.add('grabbing')
      const sx = e.clientX
      const sy = e.clientY
      const ox = this.view.x
      const oy = this.view.y
      const move = (ev: PointerEvent): void => {
        this.view.x = ox + (ev.clientX - sx)
        this.view.y = oy + (ev.clientY - sy)
        this.applyView()
      }
      const up = (): void => {
        this.canvas.classList.remove('grabbing')
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', up)
        this.savePositions()
      }
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', up)
    })
    this.canvas.addEventListener(
      'wheel',
      (e) => {
        e.preventDefault()
        const r = this.canvas.getBoundingClientRect()
        const mx = e.clientX - r.left
        const my = e.clientY - r.top
        const wx = (mx - this.view.x) / this.view.k
        const wy = (my - this.view.y) / this.view.k
        this.view.k = Math.min(2.2, Math.max(0.3, this.view.k * (e.deltaY < 0 ? 1.1 : 0.9)))
        this.view.x = mx - wx * this.view.k
        this.view.y = my - wy * this.view.k
        this.applyView()
      },
      { passive: false },
    )
  }

  private wireZoom(root: HTMLElement): void {
    root.querySelectorAll<HTMLElement>('.flow-zoom button').forEach((b) => {
      b.onclick = () => {
        const z = b.dataset.z
        if (z === 'fit') return this.fit()
        this.zoomBy(z === 'in' ? 1.15 : 0.87)
      }
    })
  }

  private zoomBy(f: number): void {
    const r = this.canvas.getBoundingClientRect()
    const mx = r.width / 2
    const my = r.height / 2
    const wx = (mx - this.view.x) / this.view.k
    const wy = (my - this.view.y) / this.view.k
    this.view.k = Math.min(2.2, Math.max(0.3, this.view.k * f))
    this.view.x = mx - wx * this.view.k
    this.view.y = my - wy * this.view.k
    this.applyView()
    this.savePositions()
  }

  private fit(): void {
    if (!this.nodes().length) return
    const pad = 56
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const n of this.nodes()) {
      const p = this.pos.get(n.id) as Pos
      minX = Math.min(minX, p.x)
      minY = Math.min(minY, p.y)
      maxX = Math.max(maxX, p.x + (n.core ? NW + 14 : NW))
      maxY = Math.max(maxY, p.y + this.nodeH(n))
    }
    minX -= pad
    minY -= pad
    maxX += pad
    maxY += pad
    const r = this.canvas.getBoundingClientRect()
    const leftGutter = 96 // clear the palette rail
    const k = Math.max(0.35, Math.min(1.4, Math.min((r.width - leftGutter - 24) / (maxX - minX), (r.height - 24) / (maxY - minY))))
    this.view.k = k
    this.view.x = leftGutter - minX * k
    this.view.y = (r.height - (maxY - minY) * k) / 2 - minY * k
    this.applyView()
    this.savePositions()
  }

  private applyView(): void {
    this.world.style.transform = `translate(${this.view.x}px, ${this.view.y}px) scale(${this.view.k})`
    const z = this.canvas.parentElement?.querySelector('.flow-zval')
    if (z) z.textContent = Math.round(this.view.k * 100) + '%'
  }

  // ---- palette --------------------------------------------------------------
  // The rail is data-driven: the host supplies groups of existing objects
  // (dragging one references it to this agent) plus "＋ new" create entries.
  private buildRail(): void {
    const rail = this.railEl
    rail.innerHTML = ''
    for (const group of this.model.palette) {
      const l = document.createElement('div')
      l.className = 'flow-rlab'
      l.textContent = group.label
      rail.appendChild(l)
      for (const entry of group.entries) {
        const b = document.createElement('button')
        b.className = 'flow-palnode'
        b.style.setProperty('--nc', cvar(entry.icon))
        b.style.setProperty('--nc-soft', `color-mix(in srgb, ${cvar(entry.icon)} 15%, var(--color-surface-raised, #fff))`)
        b.style.setProperty('--nc-line', `color-mix(in srgb, ${cvar(entry.icon)} 40%, var(--color-border-default, #ccc))`)
        const isNew = entry.id.startsWith(NEW_PREFIX)
        b.classList.toggle('linked', !!entry.linked)
        b.classList.toggle('is-new', isNew)
        if (entry.sub) b.title = entry.sub
        else if (entry.linked) b.title = 'already linked to this agent'
        b.innerHTML = `<span class="chip">${svgIcon(entry.icon)}</span><span>${esc(entry.label)}</span>${entry.linked ? '<span class="flow-pal-check">✓</span>' : ''}`
        if (entry.linked) {
          // Already referenced — inert (remove it from the agent via the node's
          // Remove button on the canvas).
          b.disabled = true
        } else if (isNew) {
          const key = entry.id.slice(NEW_PREFIX.length)
          b.classList.add('draggable')
          this.wirePaletteDrag(b, entry.icon, (world) => this.createDraft(key, world))
        } else {
          b.classList.add('draggable')
          const id = entry.id
          this.wirePaletteDrag(b, entry.icon, (world) => void this.addExistingAt(id, world))
        }
        rail.appendChild(b)
      }
    }
  }

  // Reference an existing object (dragged from the palette) to this agent, then
  // place its node where it was dropped. Mirrors doCreate's placement path.
  private async addExistingAt(id: string, pos: Pos): Promise<void> {
    const realId = await this.cb.addExisting(id)
    if (!realId) return
    // addExisting reloaded data → the real node already rendered at an
    // auto-layout spot. Move it to where the palette item was dropped.
    this.pos.set(realId, pos)
    const el = this.nodeEls.get(realId)
    if (el) {
      el.style.left = pos.x + 'px'
      el.style.top = pos.y + 'px'
    }
    this.renderWires()
    this.savePositions()
  }

  // Ghost-drag mechanics shared by create entries and existing-object entries:
  // click drops at canvas centre; drag drops where released. onDrop receives the
  // world-space position.
  private wirePaletteDrag(b: HTMLElement, icon: FNodeType, onDrop: (world: Pos) => void): void {
    b.onpointerdown = (e) => {
      e.preventDefault()
      const sx = e.clientX
      const sy = e.clientY
      let ghost: HTMLElement | null = null
      const move = (ev: PointerEvent): void => {
        if (!ghost && Math.hypot(ev.clientX - sx, ev.clientY - sy) > 6) {
          ghost = document.createElement('div')
          ghost.className = 'flow-ghost'
          ghost.style.setProperty('--nc', cvar(icon))
          ghost.innerHTML = `<span class="flow-nic">${svgIcon(icon)}</span>${TYPES[icon].label}`
          document.body.appendChild(ghost)
        }
        if (ghost) {
          ghost.style.left = ev.clientX + 'px'
          ghost.style.top = ev.clientY + 'px'
        }
      }
      const up = (ev: PointerEvent): void => {
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', up)
        if (ghost) ghost.remove()
        const r = this.canvas.getBoundingClientRect()
        const dropped = ghost !== null
        const over = ev.clientX >= r.left && ev.clientX <= r.right && ev.clientY >= r.top && ev.clientY <= r.bottom
        // click (no drag) → drop at centre; drag → drop where released (if over canvas)
        const world =
          dropped && over
            ? { x: (ev.clientX - r.left - this.view.x) / this.view.k - NW / 2, y: (ev.clientY - r.top - this.view.y) / this.view.k - 40 }
            : { x: (r.width / 2 - this.view.x) / this.view.k - NW / 2, y: (r.height / 2 - this.view.y) / this.view.k - 40 }
        if (!dropped || over) onDrop(world)
      }
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', up)
    }
  }

  // Drop an unsaved draft node, wire it to the agent where applicable, and open
  // its create dialog.
  private createDraft(key: string, pos: Pos): void {
    const spec = this.cb.draftFor(key)
    if (!spec) return
    const id = 'draft:' + key + ':' + ++this.draftSeq
    const n: FNode = { id, type: spec.nodeType, title: spec.title, ins: spec.ins, outs: spec.outs, fields: spec.fields, draft: true, canDelete: true, createKey: key }
    this.pos.set(id, pos)
    this.drafts.push(n)
    if (spec.outPort && spec.agentPort && this.node('agent')) this.draftWires.push({ from: [id, spec.outPort], to: ['agent', spec.agentPort] })
    this.syncNodes()
    this.renderWires()
    this.openEditor(id)
  }

  private discardDraft(id: string): void {
    this.drafts = this.drafts.filter((d) => d.id !== id)
    this.draftWires = this.draftWires.filter((w) => w.from[0] !== id && w.to[0] !== id)
    this.pos.delete(id)
    const el = this.nodeEls.get(id)
    if (el) {
      el.remove()
      this.nodeEls.delete(id)
    }
    this.renderWires()
  }

  private buildLegend(el: HTMLElement): void {
    const items: [FNodeType, string][] = [
      ['schedule', 'time'],
      ['trigger', 'event'],
      ['chat', 'human'],
      ['model', 'brain'],
      ['output', 'result'],
    ]
    el.innerHTML =
      `<span class="lab">Cables</span>` +
      items.map(([t, l]) => `<span class="lg"><span class="cable" style="background:${cvar(t)}"></span>${l}</span>`).join('')
  }

  // ---- position persistence -------------------------------------------------
  private storeKey(key: string): string {
    return 'kedge:agents:flow:' + key
  }
  private loadPositions(key: string): boolean {
    try {
      const raw = localStorage.getItem(this.storeKey(key))
      if (!raw) return false
      const p = JSON.parse(raw) as { nodes?: Record<string, Pos>; view?: { x: number; y: number; k: number } }
      if (p.nodes) for (const [id, xy] of Object.entries(p.nodes)) this.pos.set(id, xy)
      if (p.view) this.view = p.view
      return !!p.view
    } catch {
      return false
    }
  }
  private savePositions(): void {
    if (!this.loadedKey) return
    const nodes: Record<string, Pos> = {}
    for (const [id, xy] of this.pos) nodes[id] = xy
    try {
      localStorage.setItem(this.storeKey(this.loadedKey), JSON.stringify({ nodes, view: this.view }))
    } catch {
      /* storage full / disabled — positions just won't persist */
    }
  }
}
