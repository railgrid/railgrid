// The agent's configuration pane — one form absorbing everything that used to
// be split across Settings, Wiring and the Flow canvas' edit dialogs. Each
// section saves on its own (the API is a pointer patch, so a section save never
// touches a field it doesn't own) and applies optimistically with rollback.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { mutate } from '../mutate'
import { toast } from '../ui/toast'
import { channelInbound } from '../conn-defs'
import type { Agent, AgentChannel, AgentPatch, Autonomy } from '../types'

import './automation'

// Autonomy is enforced server-side now, so the copy states the consequence
// rather than naming the mode.
const AUTONOMY_MODES: { id: Autonomy; label: string; blurb: string }[] = [
  { id: 'suggest', label: 'Suggest', blurb: 'Every tool call waits for your approval. Safest; most interruptions.' },
  { id: 'ask', label: 'Ask', blurb: 'Only tools matched by a grant’s approval patterns wait for you.' },
  { id: 'auto', label: 'Auto', blurb: 'Tools run without asking. Use only with tools you trust unattended.' },
]

interface ChannelRow extends AgentChannel {
  // key survives re-renders so typing in one row isn't lost when another is
  // added or removed.
  key: number
}

export class AgentConfig extends StoreElement {
  @property({ type: String }) name = ''

  @state() private displayName = ''
  @state() private description = ''
  @state() private systemPrompt = ''
  @state() private modelCredential = ''
  @state() private fallbacks: string[] = []
  @state() private autonomy: Autonomy = 'ask'
  @state() private budgetUSD = ''
  @state() private budgetTokens = ''
  @state() private maxToolTurns = ''
  @state() private timeoutSeconds = ''
  @state() private channels: ChannelRow[] = []
  @state() private channelError = ''

  private hydratedFor = ''
  private rowKey = 0

  protected willUpdate(): void {
    super.willUpdate()
    const a = this.store.agent(this.name)
    if (!a || this.hydratedFor === this.name) return
    this.hydrate(a)
    this.hydratedFor = this.name
  }

  private hydrate(a: Agent): void {
    this.displayName = a.spec?.displayName || a.metadata.name
    this.description = a.spec?.description || ''
    this.systemPrompt = a.spec?.systemPrompt || ''
    this.modelCredential = a.spec?.models?.chat || ''
    this.fallbacks = [...(a.spec?.modelFallbacks || [])]
    this.autonomy = (a.spec?.autonomy as Autonomy) || 'ask'
    this.budgetUSD = a.spec?.budget?.usdLimit || ''
    this.budgetTokens = a.spec?.budget?.tokenLimit ? String(a.spec.budget.tokenLimit) : ''
    // 0 means "provider default" server-side, so it renders as an empty field.
    this.maxToolTurns = a.spec?.limits?.maxToolTurns ? String(a.spec.limits.maxToolTurns) : ''
    this.timeoutSeconds = a.spec?.limits?.timeoutSeconds ? String(a.spec.limits.timeoutSeconds) : ''
    this.channels = (a.spec?.channels || []).map((ch) => ({ ...ch, key: ++this.rowKey }))
  }

  // save applies the patch locally first (so the pane and the chat pane both
  // reflect it immediately), then writes; a failure restores the pre-edit spec.
  private async save(patch: AgentPatch, apply: (spec: Agent['spec']) => void, success: string): Promise<boolean> {
    const a = this.store.agent(this.name)
    if (!a) return false
    const before = JSON.parse(JSON.stringify(a.spec)) as Agent['spec']
    const res = await mutate(this.store, {
      run: () => this.api.patchAgent(this.name, patch),
      success,
      failure: 'Save failed',
      optimistic: () => apply(a.spec),
      rollback: () => {
        a.spec = before
        this.hydrate(a)
      },
      reload: ['agents'],
    })
    return res !== undefined
  }

  render(): TemplateResult {
    const a = this.store.agent(this.name)
    if (!a) return html`<div class="k-card agents-state agents-state-loading k-loading-reveal" role="status">Loading configuration…</div>`
    return html`
      ${this.personaSection()} ${this.modelSection(a)} ${this.policySection()} ${this.toolsSection(a)} ${this.channelsSection(a)}
      <agents-automation .store=${this.store} .api=${this.api} kind="schedule" .agent=${this.name}></agents-automation>
      <agents-automation .store=${this.store} .api=${this.api} kind="trigger" .agent=${this.name}></agents-automation>
      ${this.delegatesSection(a)}
    `
  }

  // ---- persona -------------------------------------------------------------

  private personaSection(): TemplateResult {
    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('sparkles')} Persona</h3>
      <p class="muted">Who this agent is and how it should behave on every run.</p>
      <label>
        Display name
        <input class="k-input" .value=${this.displayName} @input=${(e: Event) => (this.displayName = (e.target as HTMLInputElement).value)} />
      </label>
      <label>
        Description
        <input class="k-input"
          placeholder="What this agent is for — shown to you, not to the model."
          .value=${this.description}
          @input=${(e: Event) => (this.description = (e.target as HTMLInputElement).value)}
        />
      </label>
      <label>
        System prompt
        <textarea class="k-input"
          rows="6"
          placeholder="You are a concise assistant that…"
          .value=${this.systemPrompt}
          @input=${(e: Event) => (this.systemPrompt = (e.target as HTMLTextAreaElement).value)}
        ></textarea>
      </label>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary"
          @click=${() =>
            void this.save(
              { displayName: this.displayName.trim(), description: this.description.trim(), systemPrompt: this.systemPrompt },
              (spec) => {
                spec.displayName = this.displayName.trim()
                spec.description = this.description.trim()
                spec.systemPrompt = this.systemPrompt
              },
              'Persona saved.',
            )}
        >
          ${icon('check')} Save persona
        </button>
      </div>
    </section>`
  }

  // ---- model ---------------------------------------------------------------

  private modelSection(a: Agent): TemplateResult {
    const creds = this.store.credentials.data
    const available = creds.filter((c) => c.name !== this.modelCredential && !this.fallbacks.includes(c.name))
    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('brain')} Model</h3>
      <p class="muted">Which credential this agent reasons with. Fallbacks are tried in order when the primary fails.</p>
      <label>
        Model credential
        <select class="k-input" @change=${(e: Event) => (this.modelCredential = (e.target as HTMLSelectElement).value)}>
          <option value="">— no model —</option>
          ${creds.map((c) => html`<option value=${c.name} ?selected=${c.name === this.modelCredential}>${c.name}${c.model ? ` (${c.model})` : ''}</option>`)}
        </select>
        ${creds.length === 0
          ? html`<span class="agents-hint"
              >No models yet —
              <button type="button" class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'menu', menu: 'models' })}>
                add one under Models</button
              >.</span
            >`
          : nothing}
      </label>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Fallbacks</span>
        ${this.fallbacks.length
          ? html`<div class="agents-chiprow">
              ${this.fallbacks.map(
                (f, i) => html`<span class="agents-chip"
                  >${f}
                  <button
                    class="k-icon-action agents-chip-x"
                    aria-label="Remove fallback ${f}"
                    type="button"
                    @click=${() => (this.fallbacks = this.fallbacks.filter((_, j) => j !== i))}
                  >
                    ${icon('x')}
                  </button>
                </span>`,
              )}
            </div>`
          : html`<span class="agents-hint">None — a model failure fails the run.</span>`}
        ${available.length
          ? html`<select class="k-input agents-addselect"
              @change=${(e: Event) => {
                const sel = e.target as HTMLSelectElement
                if (sel.value) this.fallbacks = [...this.fallbacks, sel.value]
                sel.value = ''
              }}
            >
              <option value="">+ add fallback…</option>
              ${available.map((c) => html`<option value=${c.name}>${c.name}</option>`)}
            </select>`
          : nothing}
      </div>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary"
          @click=${() =>
            void this.save(
              { modelCredential: this.modelCredential, modelFallbacks: this.fallbacks },
              (spec) => {
                spec.models = { ...(spec.models || {}), chat: this.modelCredential }
                spec.modelFallbacks = [...this.fallbacks]
              },
              'Model saved.',
            )}
        >
          ${icon('check')} Save model
        </button>
        ${a.spec?.models?.chat !== this.modelCredential ? html`<span class="agents-hint">unsaved change</span>` : nothing}
      </div>
    </section>`
  }

  // ---- autonomy + budget ---------------------------------------------------

  private policySection(): TemplateResult {
    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('gauge')} Autonomy &amp; budget</h3>
      <p class="muted">
        Autonomy decides which tool calls stop and wait for you. It is enforced on every run — a paused run shows up in
        <strong>Activity</strong> as <em>PendingApproval</em>.
      </p>
      <div class="agents-radiocards">
        ${AUTONOMY_MODES.map(
          (m) => html`<label class="agents-radiocard ${m.id === this.autonomy ? 'sel' : ''}">
            <input type="radio" name="autonomy" .checked=${m.id === this.autonomy} @change=${() => (this.autonomy = m.id)} />
            <span class="agents-radiocard-t">${m.label}</span>
            <span class="agents-radiocard-b">${m.blurb}</span>
          </label>`,
        )}
      </div>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Budget</span>
        <div class="agents-grid2">
          <label>
            Monthly budget (USD)
            <input class="k-input"
              inputmode="decimal"
              placeholder="blank = unlimited"
              .value=${this.budgetUSD}
              @input=${(e: Event) => (this.budgetUSD = (e.target as HTMLInputElement).value)}
            />
          </label>
          <label>
            Monthly token cap
            <input class="k-input"
              inputmode="numeric"
              placeholder="blank = unlimited"
              .value=${this.budgetTokens}
              @input=${(e: Event) => (this.budgetTokens = (e.target as HTMLInputElement).value)}
            />
          </label>
        </div>
      </div>
      <div class="agents-fieldset">
        <span class="agents-fieldset-legend">Limits</span>
        <div class="agents-grid2">
          <label>
            Max tool turns
            <input class="k-input"
              inputmode="numeric"
              placeholder="blank = provider default"
              .value=${this.maxToolTurns}
              @input=${(e: Event) => (this.maxToolTurns = (e.target as HTMLInputElement).value)}
            />
            <span class="agents-hint">How many tool-call rounds one run may take before it stops.</span>
          </label>
          <label>
            Run timeout (seconds)
            <input class="k-input"
              inputmode="numeric"
              placeholder="blank = provider default"
              .value=${this.timeoutSeconds}
              @input=${(e: Event) => (this.timeoutSeconds = (e.target as HTMLInputElement).value)}
            />
            <span class="agents-hint">Wall-clock bound on a run — it is aborted when this elapses.</span>
          </label>
        </div>
      </div>
      <div class="agents-form-actions">
        <button class="k-btn k-btn--primary"
          @click=${() => {
            const tokens = intOrZero(this.budgetTokens)
            const turns = intOrZero(this.maxToolTurns)
            const timeout = intOrZero(this.timeoutSeconds)
            void this.save(
              {
                autonomy: this.autonomy,
                budgetUSD: this.budgetUSD.trim(),
                budgetTokens: tokens,
                maxToolTurns: turns,
                timeoutSeconds: timeout,
              },
              (spec) => {
                spec.autonomy = this.autonomy
                spec.budget = { ...(spec.budget || {}), usdLimit: this.budgetUSD.trim(), tokenLimit: tokens }
                spec.limits = { maxToolTurns: turns, timeoutSeconds: timeout }
              },
              'Autonomy, budget and limits saved.',
            )
          }}
        >
          ${icon('check')} Save policy
        </button>
      </div>
    </section>`
  }

  // ---- tools ---------------------------------------------------------------

  private toolsSection(a: Agent): TemplateResult {
    const interactive = a.spec?.tools?.interactive
    const background = a.spec?.tools?.background
    const linkedToolsets = new Set([...(interactive?.toolsets || []), ...(background?.toolsets || [])])
    const linkedTools = new Set([...(interactive?.connections || []), ...(background?.connections || [])])
    const bgToolsets = new Set(background?.toolsets || [])
    const bgTools = new Set(background?.connections || [])
    const toolConns = this.store.toolConnections()
    const spawnInteractive = (interactive?.families || []).includes('spawn')
    const spawnBackground = (background?.families || []).includes('spawn')
    const webInteractive = (interactive?.families || []).includes('web')
    const webBackground = (background?.families || []).includes('web')
    // A websearch Connection wired to this agent is what makes web_search work;
    // web_fetch alone does not need one.
    const hasSearchTool = (interactive?.connections || []).some((n) => this.store.connectionType(n) === 'websearch')

    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('wrench')} Tools &amp; toolsets</h3>
      <p class="muted">
        What this agent can call. Chat always gets a granted tool; tick <strong>background</strong> to also allow it on schedules,
        triggers and heartbeats — those run with nobody watching, so they get a deliberately smaller surface.
      </p>
      <fieldset class="agents-wire-fs">
        <legend>${icon('puzzle')} Toolsets</legend>
        ${this.store.toolsets.data.length
          ? this.store.toolsets.data.map((t) => {
              const n = t.metadata.name
              return this.grantRow(
                t.spec.displayName || n,
                nothing,
                linkedToolsets.has(n),
                bgToolsets.has(n),
                (on) => void this.setToolsetLinked(a, n, on),
                (on) => void this.setToolsetBackground(a, n, on),
              )
            })
          : html`<p class="agents-hint">
              No toolsets yet — create one in the
              <button type="button" class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'menu', menu: 'connections' })}>
                Connections
              </button>
              tab.
            </p>`}
      </fieldset>
      <fieldset class="agents-wire-fs">
        <legend>${icon('globe')} Built-in capabilities</legend>
        <p class="muted">
          Tools the agent has on its own, with nothing to wire up. Reading the web needs no connection; <strong>searching</strong> it
          needs a websearch tool granted below, and without one the agent can only read pages it is given a link to. Turning on fan-out
          also teaches the agent how to use it — you do not need to write that into the prompt.
        </p>
        ${this.grantRow(
          'Read the web',
          html`<span class="muted">web_fetch${webInteractive && !hasSearchTool ? ' — no search tool wired' : ''}</span>`,
          webInteractive,
          webBackground,
          (on) => void this.setFamily(a, 'web', 'Web access', on, false),
          (on) => void this.setFamily(a, 'web', 'Web access', on, true),
        )}
        ${this.grantRow(
          'Research fan-out',
          html`<span class="muted">spawn + join${spawnInteractive && !webInteractive ? ' — workers will have no web access' : ''}</span>`,
          spawnInteractive,
          spawnBackground,
          (on) => void this.setFamily(a, 'spawn', 'Research fan-out', on, false),
          (on) => void this.setFamily(a, 'spawn', 'Research fan-out', on, true),
        )}
        ${spawnInteractive && !webInteractive
          ? html`<p class="agents-hint agents-warn-inline">
              ${icon('circle')} This agent can spawn workers but has no web access, so a worker inherits none either — a fan-out would
              answer from the model alone. Turn on <strong>Read the web</strong>, and wire a websearch tool for real searching.
            </p>`
          : nothing}
      </fieldset>
      <fieldset class="agents-wire-fs">
        <legend>${icon('wrench')} Direct tools</legend>
        ${toolConns.length
          ? toolConns.map((c) => {
              const n = c.metadata.name
              return this.grantRow(
                c.spec.displayName || n,
                html`<span class="muted">${c.spec.type}</span>`,
                linkedTools.has(n),
                bgTools.has(n),
                (on) => void this.setToolLinked(a, n, on),
                (on) => void this.setToolBackground(a, n, on),
              )
            })
          : html`<p class="agents-hint">
              No tools yet — add a GitHub / MCP / web-search connection under
              <button type="button" class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'menu', menu: 'connections' })}>
                Connections</button
              >.
            </p>`}
      </fieldset>
    </section>`
  }

  private grantRow(
    label: string,
    suffix: unknown,
    linked: boolean,
    background: boolean,
    onLink: (on: boolean) => void,
    onBackground: (on: boolean) => void,
  ): TemplateResult {
    return html`<div class="agents-tool-row">
      <label class="agents-check">
        <input type="checkbox" .checked=${linked} @change=${(e: Event) => onLink((e.target as HTMLInputElement).checked)} />
        ${label} ${suffix}
      </label>
      <label
        class="agents-check agents-bg-toggle"
        title="Background runs (schedules, triggers, heartbeats) have no human watching, so tools stay interactive-only unless opted in here."
      >
        <input
          type="checkbox"
          .checked=${background}
          ?disabled=${!linked}
          @change=${(e: Event) => onBackground((e.target as HTMLInputElement).checked)}
        />
        ${icon('clock')} background
      </label>
    </div>`
  }

  private setToolsetLinked(a: Agent, ts: string, on: boolean): Promise<boolean> {
    const inter = a.spec?.tools?.interactive?.toolsets || []
    const bg = a.spec?.tools?.background?.toolsets || []
    const nextInter = on ? [...new Set([...inter, ts])] : inter.filter((x) => x !== ts)
    // Unlinking clears the background grant too — a background-only grant would
    // be invisible in this UI.
    const nextBg = on ? bg : bg.filter((x) => x !== ts)
    return this.save(
      { interactiveToolsets: nextInter, backgroundToolsets: nextBg },
      (spec) => setGrants(spec, { interactiveToolsets: nextInter, backgroundToolsets: nextBg }),
      on ? 'Toolset linked.' : 'Toolset unlinked.',
    )
  }

  private setToolsetBackground(a: Agent, ts: string, on: boolean): Promise<boolean> {
    const bg = a.spec?.tools?.background?.toolsets || []
    const next = on ? [...new Set([...bg, ts])] : bg.filter((x) => x !== ts)
    return this.save(
      { backgroundToolsets: next },
      (spec) => setGrants(spec, { backgroundToolsets: next }),
      on ? 'Toolset enabled for background runs.' : 'Toolset is now interactive-only.',
    )
  }

  // Families are DERIVED from the wired connections, never hand-picked — the
  // one concept the UI exposes is the Tool object itself.
  private setToolLinked(a: Agent, cn: string, on: boolean): Promise<boolean> {
    const inter = a.spec?.tools?.interactive?.connections || []
    const bg = a.spec?.tools?.background?.connections || []
    const nextInter = on ? [...new Set([...inter, cn])] : inter.filter((x) => x !== cn)
    const nextBg = on ? bg : bg.filter((x) => x !== cn)
    const patch: AgentPatch = {
      interactiveConnections: nextInter,
      backgroundConnections: nextBg,
      interactiveFamilies: this.store.familiesFor(nextInter, a.spec?.tools?.interactive?.families),
      backgroundFamilies: this.store.familiesFor(nextBg, a.spec?.tools?.background?.families),
    }
    return this.save(patch, (spec) => setGrants(spec, patch), on ? 'Tool granted.' : 'Tool removed.')
  }

  private setToolBackground(a: Agent, cn: string, on: boolean): Promise<boolean> {
    const bg = a.spec?.tools?.background?.connections || []
    const next = on ? [...new Set([...bg, cn])] : bg.filter((x) => x !== cn)
    const patch: AgentPatch = {
      backgroundConnections: next,
      backgroundFamilies: this.store.familiesFor(next, a.spec?.tools?.background?.families),
    }
    return this.save(
      patch,
      (spec) => setGrants(spec, patch),
      on ? 'Tool enabled for background runs.' : 'Tool is now interactive-only.',
    )
  }

  // Standalone families (web, spawn) are capabilities of the agent rather than
  // wired connections, so they are toggled directly. web_fetch in particular
  // needs no connection at all, which is why "web" cannot only come from a
  // websearch Connection.
  private setFamily(a: Agent, family: string, label: string, on: boolean, background: boolean): Promise<boolean> {
    const withFamily = (fams: string[] | undefined, enabled: boolean): string[] => {
      const set = new Set(fams && fams.length ? fams : ['core'])
      if (enabled) set.add(family)
      else set.delete(family)
      return [...set]
    }
    const inter = a.spec?.tools?.interactive?.families
    const bg = a.spec?.tools?.background?.families
    const patch: AgentPatch = background
      ? { backgroundFamilies: withFamily(bg, on) }
      : {
          interactiveFamilies: withFamily(inter, on),
          // Turning it off entirely also clears the background grant, so a
          // background-only grant can't linger invisibly.
          ...(on ? {} : { backgroundFamilies: withFamily(bg, false) }),
        }
    const msg = background
      ? on
        ? `${label} enabled for background runs.`
        : `${label} is now interactive-only.`
      : on
        ? `${label} enabled.`
        : `${label} disabled.`
    return this.save(patch, (spec) => setGrants(spec, patch), msg)
  }

  // ---- channels ------------------------------------------------------------

  private channelsSection(a: Agent): TemplateResult {
    const conns = this.store.channelConnections()
    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('megaphone')} Channels</h3>
      <p class="muted">
        Where this agent messages you — and, for chat channels, where you message it. Bind a <strong>primary</strong> channel plus any
        secondaries (a dedicated incidents or news channel, say); schedules and triggers can route to any of them by name.
      </p>
      ${conns.length === 0
        ? html`<p class="agents-hint">
            No channels yet — add a Telegram / Slack / Discord / email connection under
            <button type="button" class="k-btn k-btn--ghost agents-linkbtn" @click=${() => this.navigate({ kind: 'menu', menu: 'connections' })}>
              Connections</button
            >.
          </p>`
        : nothing}
      <div class="agents-chan-editor">
        ${repeat(
          this.channels,
          (r) => r.key,
          (r) => html`<div class="agents-chan-row">
            <input class="k-input agents-chan-name"
              placeholder="primary"
              aria-label="Channel role name"
              .value=${r.name || ''}
              @input=${(e: Event) => this.patchRow(r.key, { name: (e.target as HTMLInputElement).value })}
            />
            <select class="k-input agents-chan-conn"
              aria-label="Channel connection"
              @change=${(e: Event) => this.patchRow(r.key, { connectionRef: (e.target as HTMLSelectElement).value })}
            >
              <option value="">— pick a connection —</option>
              ${conns.map(
                (c) => html`<option value=${c.metadata.name} ?selected=${c.metadata.name === r.connectionRef}
                  >${c.spec.displayName || c.metadata.name} (${c.spec.type})</option
                >`,
              )}
            </select>
            <label class="agents-chan-primary" title="Default channel for output with no channel set">
              <input
                type="radio"
                name="chan-primary"
                .checked=${!!r.primary}
                @change=${() => (this.channels = this.channels.map((x) => ({ ...x, primary: x.key === r.key })))}
              />
              primary
            </label>
            <button
              class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger"
              aria-label="Remove channel ${r.name || ''}"
              title="Remove channel"
              @click=${() => (this.channels = this.channels.filter((x) => x.key !== r.key))}
            >
              ${icon('trash')}
            </button>
          </div>`,
        )}
      </div>
      ${this.channelError ? html`<div class="agents-fielderr">${this.channelError}</div>` : nothing}
      <div class="agents-form-actions">
        <button
          class="k-btn k-btn--ghost secondary"
          ?disabled=${conns.length === 0}
          @click=${() =>
            (this.channels = [
              ...this.channels,
              { key: ++this.rowKey, name: this.nextChannelName(), connectionRef: '', primary: this.channels.length === 0 },
            ])}
        >
          ${icon('plus')} Add channel
        </button>
        <button class="k-btn k-btn--primary" @click=${() => void this.saveChannels()}>${icon('check')} Save channels</button>
      </div>
      ${this.inboundLines(a)}
    </section>`
  }

  private patchRow(key: number, patch: Partial<AgentChannel>): void {
    this.channels = this.channels.map((r) => (r.key === key ? { ...r, ...patch } : r))
  }

  private async saveChannels(): Promise<void> {
    const trimmed = this.channels.map((r) => ({ name: r.name.trim(), connectionRef: r.connectionRef.trim(), primary: !!r.primary }))
    // A row the user never touched is ignored; a half-filled one is an error.
    // Silently dropping it (the old behaviour) reported "saved" while binding
    // nothing — the agent then looked bound in the UI but no channel reached it.
    const rows = trimmed.filter((r) => r.name || r.connectionRef)
    for (const r of rows) {
      if (!r.connectionRef) {
        this.channelError = `Channel “${r.name}” has no connection — pick one, or remove the row.`
        return
      }
      if (!r.name) {
        this.channelError = 'Every channel needs a role name (for example “primary”).'
        return
      }
    }
    const seen = new Set<string>()
    for (const r of rows) {
      if (seen.has(r.name)) {
        this.channelError = `Duplicate channel name “${r.name}” — names must be unique.`
        return
      }
      seen.add(r.name)
    }
    this.channelError = ''
    await this.save({ channels: rows }, (spec) => (spec.channels = rows), 'Channels saved.')
  }

  // nextChannelName suggests the role name for a new row so the common case
  // (one channel, called "primary") needs no typing at all.
  private nextChannelName(): string {
    const used = new Set(this.channels.map((r) => r.name.trim()).filter(Boolean))
    if (!used.has('primary')) return 'primary'
    for (let i = 2; ; i++) {
      const candidate = `channel${i}`
      if (!used.has(candidate)) return candidate
    }
  }

  // inboundLines shows, per bound channel, whether the agent can actually
  // receive on it and offers the enable/test actions.
  private inboundLines(a: Agent): TemplateResult | typeof nothing {
    const bound = a.spec?.channels || []
    if (!bound.length) return nothing
    const conns = this.store.channelConnections()
    return html`<div class="agents-chan-inbound">
      ${bound.map((ch) => {
        const conn = conns.find((c) => c.metadata.name === ch.connectionRef)
        if (!conn) {
          return html`<div class="agents-inbound-line">
            <span class="k-badge agents-badge">${ch.name}</span>
            <span class="muted">Connection “${ch.connectionRef}” not found — pick one above and save.</span>
          </div>`
        }
        const inb = channelInbound(conn)
        return html`<div class="agents-inbound-line">
          <span class="k-badge agents-badge ${inb.on ? 'agents-cat-channel' : ''}"
            >${ch.name}${ch.primary ? ' ★' : ''} · ${icon('swap')} inbound ${inb.on ? 'on' : 'off'}</span
          >
          <span class="muted">${inb.note}</span>
          <span class="agents-inbound-actions">
            ${inb.canEnable
              ? html`<button class="k-btn k-btn--ghost secondary" @click=${() => void this.enableInbound(ch.connectionRef)}>Enable inbound</button>`
              : nothing}
            <button class="k-btn k-btn--ghost secondary" @click=${() => void this.testChannel(ch.connectionRef)}>${icon('send')} Test</button>
          </span>
        </div>`
      })}
    </div>`
  }

  private async enableInbound(name: string): Promise<void> {
    const res = await mutate(this.store, {
      run: () => this.api.enableInbound(name),
      failure: 'Enable inbound failed',
      reload: ['connections'],
    })
    if (res) toast(res.registered ? 'ok' : 'info', `${res.note} ${res.webhookURL}`)
  }

  // A failed channel test answers with an HTTP error whose body carries the send
  // failure (invalid token, bot not in channel, …); mutate surfaces that text.
  private async testChannel(name: string): Promise<void> {
    await mutate(this.store, {
      run: () => this.api.testConnection(name),
      success: `Test message sent via ${name}. Check the channel.`,
      failure: `Test of “${name}” failed`,
    })
  }

  // ---- delegates -----------------------------------------------------------

  private delegatesSection(a: Agent): TemplateResult | typeof nothing {
    const others = this.store.agents.data.filter((x) => x.metadata.name !== this.name)
    if (!others.length) return nothing
    const current = new Set(a.spec?.delegates || [])
    return html`<section class="agents-panel k-card agents-config-sec">
      <h3>${icon('corner-down-right')} Delegates</h3>
      <p class="muted">Agents this one may hand work to. A delegated run bills against this agent's budget.</p>
      <div class="agents-checkrow">
        ${others.map(
          (o) => html`<label class="agents-check">
            <input
              type="checkbox"
              .checked=${current.has(o.metadata.name)}
              @change=${(e: Event) => {
                const on = (e.target as HTMLInputElement).checked
                const next = on
                  ? [...new Set([...(a.spec?.delegates || []), o.metadata.name])]
                  : (a.spec?.delegates || []).filter((d) => d !== o.metadata.name)
                void this.save({ delegates: next }, (spec) => (spec.delegates = next), 'Delegates saved.')
              }}
            />
            ${o.spec?.displayName || o.metadata.name}
          </label>`,
        )}
      </div>
    </section>`
  }
}

// intOrZero parses a numeric field where blank/garbage means "0", which the
// backend reads as "use the provider default" (budget: unlimited).
function intOrZero(v: string): number {
  const n = Number(v.trim())
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

// setGrants mirrors a tool patch onto the local spec so the optimistic update
// matches what the server will store.
function setGrants(spec: Agent['spec'], patch: AgentPatch): void {
  spec.tools = spec.tools || {}
  spec.tools.interactive = spec.tools.interactive || {}
  spec.tools.background = spec.tools.background || {}
  if (patch.interactiveToolsets) spec.tools.interactive.toolsets = patch.interactiveToolsets
  if (patch.backgroundToolsets) spec.tools.background.toolsets = patch.backgroundToolsets
  if (patch.interactiveConnections) spec.tools.interactive.connections = patch.interactiveConnections
  if (patch.backgroundConnections) spec.tools.background.connections = patch.backgroundConnections
  if (patch.interactiveFamilies) spec.tools.interactive.families = patch.interactiveFamilies
  if (patch.backgroundFamilies) spec.tools.background.families = patch.backgroundFamilies
}

if (!customElements.get('agents-agent-config')) customElements.define('agents-agent-config', AgentConfig)
