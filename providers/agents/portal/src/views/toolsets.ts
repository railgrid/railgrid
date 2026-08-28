// Toolsets — shared bundles of Tools that agents link, rendered as a section of
// the Connections tab. Checked tool-connections drive the DERIVED families; the
// families list is never hand-picked.

import { html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { StoreElement } from '../ui/base'
import { icon } from '../ui/icon'
import { errorState } from '../ui/states'
import { confirmModal } from '../portalkit/modal'
import { mutate } from '../mutate'
import type { CreateSuccessDetail } from '../router'
import type { Toolset } from '../types'

export class Toolsets extends StoreElement {
  @property({ type: Boolean }) routeOwned = false
  @property({ type: Boolean }) createRoute = false
  // editing is null when closed, otherwise the existing toolset being edited.
  @state() private editing: string | null = null
  @state() private draftName = ''
  @state() private draftDisplay = ''
  @state() private draftConns: string[] = []

  private openCreate(): void {
    if (this.routeOwned) {
      this.navigate({ kind: 'create', resource: 'toolset' })
      return
    }
    this.editing = ''
    this.draftName = ''
    this.draftDisplay = ''
    this.draftConns = []
  }

  private openEdit(t: Toolset): void {
    this.editing = t.metadata.name
    this.draftName = t.metadata.name
    this.draftDisplay = t.spec.displayName || ''
    this.draftConns = [...(t.spec.connections || [])]
  }

  private async save(e: Event): Promise<void> {
    e.preventDefault()
    const form = e.currentTarget as HTMLFormElement
    // Read the create id from the form as well as the draft state. This keeps
    // the route surface correct for native form submission and for callers
    // that set an input before dispatching submit in an embedded host.
    const name = (form.querySelector<HTMLInputElement>('[name=name]')?.value || this.draftName).trim()
    const connections = this.draftConns
    const families = this.store.familiesFor(connections)
    const displayName = this.draftDisplay.trim()
    const editing = this.editing
    const res = editing
      ? await mutate(this.store, {
          run: () => this.api.patchToolset(editing, { displayName, families, connections }),
          success: 'Toolset updated.',
          failure: 'Update failed',
          reload: ['toolsets'],
        })
      : await mutate(this.store, {
          run: () => this.api.createToolset({ name, displayName, families, connections }),
          success: 'Toolset created.',
          failure: 'Create failed',
          reload: ['toolsets'],
        })
    if (res && this.routeOwned && !editing) {
      this.dispatchEvent(
        new CustomEvent<CreateSuccessDetail>('agents-create-success', {
          detail: { resource: 'toolset', name, item: res },
          bubbles: true,
          composed: true,
        }),
      )
    } else if (res) {
      this.editing = null
    }
  }

  private async del(name: string): Promise<void> {
    const authority = this.captureAuthority()
    const ok = await confirmModal({
      title: `Delete toolset “${name}”?`,
      message: 'Agents linking it will lose those tools.',
      danger: true,
      confirmLabel: 'Delete',
    })
    if (!ok || !this.authorityIsCurrent(authority)) return
    await mutate(authority.store, {
      run: () => authority.api.deleteToolset(name),
      success: 'Toolset deleted.',
      failure: 'Delete failed',
      reload: ['toolsets'],
    })
  }

  private usedBy(name: string): number {
    return this.store.agents.data.filter((a) =>
      [...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])].includes(name),
    ).length
  }

  render(): TemplateResult {
    if (this.createRoute) return this.createSurface()
    const slice = this.store.toolsets
    return html`<div class="agents-panel k-card agents-route-panel">
      <div class="agents-panel-head">
        <h3>${icon('package')} Toolsets</h3>
        ${this.editing === null ? html`<button class="k-btn k-btn--ghost secondary" @click=${() => this.openCreate()}>${icon('plus')} New toolset</button>` : nothing}
      </div>
      <p class="muted">Shared bundles of Tools. Define once, link from any agent's Config pane.</p>
      ${slice.error
        ? errorState(slice.error, () => void this.store.load('toolsets'))
        : slice.data.length === 0
          ? html`<p class="agents-hint">${icon('package')} No toolsets yet.</p>`
          : html`<div class="agents-tablewrap k-table">
              <table class="agents-table">
                <thead>
                  <tr><th>Name</th><th>Tools</th><th>Used by</th><th class="agents-th-actions">Actions</th></tr>
                </thead>
                <tbody>
                  ${repeat(
                    slice.data,
                    (t) => t.metadata.name,
                    (t) => {
                      const conns = t.spec.connections || []
                      const used = this.usedBy(t.metadata.name)
                      return html`<tr class=${this.editing === t.metadata.name ? 'is-editing' : ''}>
                        <td>
                          <strong>${t.spec.displayName || t.metadata.name}</strong>
                          ${t.spec.displayName ? html`<span class="agents-hint"> ${t.metadata.name}</span>` : nothing}
                        </td>
                        <td>${conns.length ? conns.map((c) => html`<span class="k-badge agents-badge">${c}</span>`) : html`<span class="muted">—</span>`}</td>
                        <td class="muted">${used} agent${used === 1 ? '' : 's'}</td>
                        <td class="agents-row-actions">
                          <button class="k-btn k-btn--ghost agents-iconbtn" aria-label="Edit ${t.metadata.name}" title="Edit" @click=${() => this.openEdit(t)}>
                            ${icon('pencil')}
                          </button>
                          <button
                            class="k-btn k-btn--ghost agents-iconbtn agents-iconbtn-danger"
                            aria-label="Delete ${t.metadata.name}"
                            title="Delete"
                            @click=${() => void this.del(t.metadata.name)}
                          >
                            ${icon('trash')}
                          </button>
                        </td>
                      </tr>`
                    },
                  )}
                </tbody>
              </table>
            </div>`}
      ${this.editing !== null ? this.form() : nothing}
    </div>`
  }

  private createSurface(): TemplateResult {
    return html`<div class="agents-menu agents-create-page k-create-page">
      <button type="button" class="k-btn k-btn--ghost k-back-action" @click=${() => this.cancelCreate()}>${icon('arrow-left')} Connections</button>
      <header class="k-create-header"><h1 class="k-create-title">Create toolset</h1><p class="k-create-description">Bundle reusable tools once, then attach the toolset to any agent.</p></header>
      ${this.form()}
    </div>`
  }

  private form(): TemplateResult {
    const toolConns = this.store.toolConnections()
    const isEdit = !!this.editing
    return html`<form class=${this.createRoute ? 'agents-toolset-form k-create-surface' : 'agents-toolset-form k-card'} @submit=${(e: Event) => void this.save(e)}>
      <div class=${this.createRoute ? 'k-create-body' : ''}>
      ${this.createRoute ? nothing : html`<h4>${isEdit ? html`Edit toolset <code>${this.editing}</code>` : 'New toolset'}</h4>`}
      ${isEdit
        ? html`<label>Display name<input class="k-input" .value=${this.draftDisplay} @input=${(e: Event) => (this.draftDisplay = (e.target as HTMLInputElement).value)} /></label>`
        : html`<div class="agents-grid2">
            <label
              >Name *<input class="k-input"
                name="name"
                required
                pattern="[a-z0-9-]+"
                placeholder="dev-tools"
                .value=${this.draftName}
                @input=${(e: Event) => (this.draftName = (e.target as HTMLInputElement).value)}
            /></label>
            <label
              >Display name<input class="k-input"
                placeholder="optional"
                .value=${this.draftDisplay}
                @input=${(e: Event) => (this.draftDisplay = (e.target as HTMLInputElement).value)}
            /></label>
          </div>`}
      <fieldset class="agents-tools">
        <legend>Tools</legend>
        <div class="agents-checkrow">
          ${toolConns.length
            ? toolConns.map(
                (c) => html`<label class="agents-check">
                  <input
                    type="checkbox"
                    .checked=${this.draftConns.includes(c.metadata.name)}
                    @change=${(e: Event) => {
                      const on = (e.target as HTMLInputElement).checked
                      this.draftConns = on
                        ? [...this.draftConns, c.metadata.name]
                        : this.draftConns.filter((x) => x !== c.metadata.name)
                    }}
                  />
                  ${c.metadata.name} <span class="agents-hint">${c.spec.type}</span>
                </label>`,
              )
            : html`<span class="muted">No tools yet — create MCP/GitHub/web tools above. Cluster edges are always on.</span>`}
        </div>
        <span class="agents-hint">Tool families are derived from these connections — never picked by hand.</span>
      </fieldset>
      </div>
      <div class=${this.createRoute ? 'k-create-actions' : 'agents-form-actions'}>
        <button type="button" class="k-btn k-btn--ghost secondary" @click=${() => this.cancelCreate()}>Cancel</button>
        <button class="k-btn k-btn--primary" type="submit">${isEdit ? 'Save' : 'Create toolset'}</button>
      </div>
    </form>`
  }

  private cancelCreate(): void {
    if (this.createRoute) {
      this.dispatchEvent(new CustomEvent('agents-cancel', { bubbles: true, composed: true }))
      return
    }
    this.editing = null
  }
}

if (!customElements.get('agents-toolsets')) customElements.define('agents-toolsets', Toolsets)
