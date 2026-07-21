// Toolsets menu: shared bundles of Tools that agents link. Define once, attach
// to many agents (in each agent's Flow, drag the toolset onto the agent).
// Checked tool-connections drive the derived families.

import type { ViewCtx } from '../view'
import { escapeHTML } from '../types'
import { createToolset, updateToolset, deleteToolset } from '../actions'

// View-local edit state.
let toolsetEdit: string | null = null

export function render(vc: ViewCtx): string {
  const toolConns = vc.store.toolConnections()
  const usedByCount = (name: string): number =>
    vc.store.agents.filter((a) => [...(a.spec?.tools?.interactive?.toolsets || []), ...(a.spec?.tools?.background?.toolsets || [])].includes(name)).length
  const rows =
    vc.store.toolsets.length === 0
      ? `<tr class="agents-empty-row"><td colspan="4"><span class="agents-empty">🧰 No toolsets yet — create one below or in an agent's Flow view.</span></td></tr>`
      : vc.store.toolsets
          .map((t) => {
            const conns = t.spec.connections || []
            const used = usedByCount(t.metadata.name)
            return `<tr>
              <td><strong>${escapeHTML(t.spec.displayName || t.metadata.name)}</strong>${t.spec.displayName ? `<span class="agents-hint"> ${escapeHTML(t.metadata.name)}</span>` : ''}</td>
              <td>${conns.length ? conns.map((c) => `<span class="agents-badge">${escapeHTML(c)}</span>`).join(' ') : '<span class="muted">—</span>'}</td>
              <td class="muted">${used} agent${used === 1 ? '' : 's'}</td>
              <td class="agents-row-actions">
                <button class="agents-iconbtn" data-edittoolset="${escapeHTML(t.metadata.name)}" title="Edit">✏️</button>
                <button class="agents-iconbtn agents-iconbtn-danger" data-deltoolset="${escapeHTML(t.metadata.name)}" title="Delete">🗑</button>
              </td>
            </tr>`
          })
          .join('')
  const editing = vc.store.toolsets.find((t) => t.metadata.name === toolsetEdit)
  const connChecks = (on: Set<string>): string =>
    toolConns.length
      ? toolConns
          .map(
            (c) => `<label class="agents-check"><input type="checkbox" name="connection" value="${escapeHTML(c.metadata.name)}" ${on.has(c.metadata.name) ? 'checked' : ''} /> ${escapeHTML(c.metadata.name)} <span class="agents-hint">${escapeHTML(c.spec.type)}</span></label>`,
          )
          .join('')
      : `<span class="muted">No tools yet — create MCP/GitHub/web tools under 🔌 Connections. Cluster edges are always on.</span>`
  const form = editing
    ? `<form class="agents-toolset-form" data-edit="${escapeHTML(editing.metadata.name)}">
        <h4>Edit toolset <code>${escapeHTML(editing.metadata.name)}</code></h4>
        <label>Display name<input name="displayName" value="${escapeHTML(editing.spec.displayName || '')}" /></label>
        <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">${connChecks(new Set(editing.spec.connections || []))}</div></fieldset>
        <div class="agents-form-actions"><button>Save</button><button type="button" class="secondary" data-toolsetcancel>Cancel</button></div>
      </form>`
    : `<form class="agents-toolset-form">
        <h4>New toolset</h4>
        <div class="agents-grid2">
          <label>Name<input name="name" required pattern="[a-z0-9-]+" placeholder="dev-tools" /></label>
          <label>Display name<input name="displayName" placeholder="optional" /></label>
        </div>
        <fieldset class="agents-tools"><legend>Tools</legend><div class="agents-checkrow">${connChecks(new Set())}</div></fieldset>
        <button>Create toolset</button>
      </form>`
  return `
    <div class="agents-panel agents-form-panel">
      <h3>Toolsets</h3>
      <p class="muted">Shared bundles of Tools that agents link. Define once, attach to many agents (in each agent's Flow, drag the toolset onto the agent).</p>
      <table class="agents-table">
        <thead><tr><th>Name</th><th>Tools</th><th>Used by</th><th class="agents-th-actions">Actions</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
      ${form}
    </div>`
}

export function wire(vc: ViewCtx, root: HTMLElement): void {
  root.querySelectorAll<HTMLElement>('[data-edittoolset]').forEach((el) =>
    el.addEventListener('click', () => {
      toolsetEdit = el.dataset.edittoolset!
      vc.rerender()
    }),
  )
  root.querySelector<HTMLElement>('[data-toolsetcancel]')?.addEventListener('click', () => {
    toolsetEdit = null
    vc.rerender()
  })
  root.querySelectorAll<HTMLElement>('[data-deltoolset]').forEach((el) =>
    el.addEventListener('click', () => {
      if (confirm(`Delete toolset ${el.dataset.deltoolset}? Agents linking it will lose those tools.`)) void deleteToolset(vc, el.dataset.deltoolset!)
    }),
  )
  const form = root.querySelector<HTMLFormElement>('.agents-toolset-form')
  form?.addEventListener('submit', (e) => {
    e.preventDefault()
    const connections = Array.from(form.querySelectorAll<HTMLInputElement>('input[name=connection]:checked')).map((i) => i.value)
    const families = vc.store.familiesFor(connections) // derived, never hand-picked
    const displayName = (form.querySelector<HTMLInputElement>('input[name=displayName]')?.value || '').trim()
    const editName = form.dataset.edit
    if (editName) {
      void updateToolset(vc, editName, { displayName, families, connections })
      toolsetEdit = null
    } else {
      const name = (form.querySelector<HTMLInputElement>('input[name=name]')?.value || '').trim()
      if (name) void createToolset(vc, { name, displayName, families, connections })
    }
  })
}
