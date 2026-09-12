import { ensureFarosUIStyles } from './portalkit/styles';
import { type FarosContext } from './api';
import { API, type Resource } from './api';
import styles from './style.css?inline';
const escape = (v: unknown) => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
class LinearElement extends HTMLElement {
    private context?: FarosContext;
    private abort = new AbortController();
    private connections: Resource[] = [];
    private selected = '';
    private team = '';
    private cursor = '';
    private namespace = 'default';
    private busy = false;
    set farosContext(value: FarosContext) { const changed = this.context?.tenant !== value.tenant; this.context = value; if (changed || !this.innerHTML) {
        this.abort.abort();
        this.abort = new AbortController();
        this.busy = false;
        this.connections = [];
        this.selected = '';
        this.team = '';
        this.render();
        void this.refresh();
    } }
    connectedCallback() { if (!this.innerHTML)
        this.render(); }
    disconnectedCallback() { this.abort.abort(); }
    private api() { if (!this.context?.tenant)
        throw new Error('Select a workspace to use Linear.'); return new API(this.context, this.namespace, this.abort.signal); }
    private value(id: string) { return this.querySelector<HTMLInputElement>(`#linear-${id}`)?.value || ''; }
    private render() {
        this.innerHTML = `<main class="linear-page"><header class="linear-page-header"><div><h1 class="linear-page-title">Linear</h1><p class="linear-page-subtitle">Plan and update issues in your workspace.</p></div><button class="k-btn k-btn--ghost" data-action="refresh">Refresh</button></header>
    <p role="status" aria-live="polite" data-message></p>
    <section class="k-card linear-section"><h2>Connection</h2><div class="linear-fields"><label>Namespace<input class="k-input" id="linear-namespace" value="${escape(this.namespace)}"></label><label>Connection<select class="k-input" id="linear-connection"><option value="">Select connection</option>${this.connections.map(c => `<option value="${escape(c.metadata.name)}" ${c.metadata.name === this.selected ? 'selected' : ''}>${escape(c.metadata.name)} · ${c.status?.ready ? 'Ready' : 'Not ready'}</option>`).join('')}</select></label><button class="k-btn k-btn--ghost" data-action="teams">Discover teams</button></div>
    <details><summary>Add a connection</summary><p>Reference an existing Secret in this namespace with an <code>apiKey</code> entry. Keep the key in Credentials.</p><form data-form="connection" class="linear-fields"><label>Name<input class="k-input" name="name" required pattern="[a-z0-9][a-z0-9-]*"></label><label>Secret name<input class="k-input" name="secret" required></label><label>Allowed team UUIDs<input class="k-input" name="teams" placeholder="Comma-separated; blank allows all accessible teams"></label><button class="k-btn k-btn--primary">Add connection</button></form></details></section>
    <section class="k-card linear-section"><header class="linear-row"><h2>Issues</h2><button class="k-btn k-btn--ghost" data-action="issues">Search</button></header><div class="linear-fields"><label>Team UUID<input class="k-input" id="linear-team" value="${escape(this.team)}"></label><label>Title contains<input class="k-input" id="linear-query" placeholder="Search issues"></label><button class="k-btn k-btn--ghost" data-action="states">Workflow states</button></div><div data-results><p class="k-text-muted">Select a connection and discover its teams to browse issues.</p></div><button class="k-btn k-btn--ghost" data-action="next" hidden>Next page</button></section>
    <section class="k-card linear-section"><h2>Change an issue</h2><p>Updates are explicit. Leave update fields blank to preserve their current values.</p><form data-form="issue" class="linear-fields"><label>Action<select class="k-input" name="action"><option value="createIssue">Create issue</option><option value="updateIssue">Update issue</option><option value="addComment">Add comment</option><option value="comments">Read comments</option></select></label><label>Issue ID<input class="k-input" name="issueID" placeholder="Required for existing issues"></label><label>Title<input class="k-input" name="title" maxlength="255"></label><label>State UUID<input class="k-input" name="stateID"></label><label class="linear-wide">Description or comment<textarea class="k-input" name="text" maxlength="16000" rows="4"></textarea></label><button class="k-btn k-btn--primary">Submit</button></form></section>
    <section class="k-card linear-section"><header class="linear-row"><h2>Operations and events</h2><div><button class="k-btn k-btn--ghost" data-action="operations">Operations</button> <button class="k-btn k-btn--ghost" data-action="events">Events</button></div></header><div data-history><p class="k-text-muted">Inspect pending or uncertain operations here before repeating a write.</p></div></section></main>`;
        this.querySelectorAll<HTMLButtonElement>('[data-action]').forEach(b => b.addEventListener('click', () => void this.run(b.dataset.action!)));
        this.querySelector<HTMLSelectElement>('#linear-connection')?.addEventListener('change', () => { this.selected = this.value('connection'); this.cursor = ''; this.team = ''; this.querySelector<HTMLInputElement>('#linear-team')!.value = ''; this.querySelector('[data-results]')!.textContent = 'Discover teams for this connection.'; });
        this.querySelector<HTMLInputElement>('#linear-namespace')?.addEventListener('change', () => { this.namespace = this.value('namespace') || 'default'; this.abort.abort(); this.abort = new AbortController(); this.busy = false; this.selected = ''; this.connections = []; this.team = ''; this.cursor = ''; this.render(); void this.refresh(); });
        this.querySelectorAll<HTMLFormElement>('form').forEach(f => f.addEventListener('submit', event => { event.preventDefault(); void this.submit(f); }));
    }
    private message(text: string) { const p = this.querySelector('[data-message]'); if (p)
        p.textContent = text; }
    private async guard(fn: () => Promise<void>) { if (this.busy)
        return; this.busy = true; const active = this.abort; this.querySelectorAll<HTMLButtonElement>('button').forEach(b => b.disabled = true); try {
        this.message('Working…');
        await fn();
        if (active === this.abort)
            this.message('Updated.');
    }
    catch (e) {
        if (active === this.abort && !(e instanceof DOMException && e.name === 'AbortError'))
            this.message(e instanceof Error ? e.message : 'Request failed.');
    }
    finally {
        if (active === this.abort) {
            this.busy = false;
            this.querySelectorAll<HTMLButtonElement>('button').forEach(b => b.disabled = false);
        }
    } }
    private async refresh() { await this.guard(async () => { const list = await this.api().list('connections'); this.connections = list.items; this.render(); }); }
    private async run(action: string) {
        if (action === 'refresh')
            return this.refresh();
        await this.guard(async () => {
            if (action === 'operations' || action === 'events') {
                const list = await this.api().list(action);
                this.querySelector('[data-history]')!.innerHTML = list.items.length ? `<ul>${list.items.map(i => `<li><strong>${escape(i.metadata.name)}</strong> ${escape(i.status?.phase || i.spec?.type || '')} ${escape(i.status?.message || '')}</li>`).join('')}</ul>` : '<p>No records yet.</p>';
                return;
            }
            if (!this.selected)
                throw new Error('Select a connection first.');
            this.team = this.value('team');
            const op = action === 'next' ? 'issues' : action;
            const result = await this.api().operation(this.selected, { action: op, teamID: this.team, query: this.value('query'), first: 25, after: action === 'next' ? this.cursor : undefined });
            this.cursor = result.pageInfo?.endCursor || '';
            this.querySelector<HTMLButtonElement>('[data-action="next"]')!.hidden = !(op === 'issues' && result.pageInfo?.hasNextPage);
            const nodes = result.nodes || [];
            this.querySelector('[data-results]')!.innerHTML = nodes.length ? `<div class="linear-table"><table class="k-table"><thead><tr><th>Identifier</th><th>Name</th><th>ID</th></tr></thead><tbody>${nodes.map(n => `<tr><td>${escape(n.identifier || n.key)}</td><td>${escape(n.title || n.name)}</td><td><code>${escape(n.id)}</code></td></tr>`).join('')}</tbody></table></div>` : '<p>No matching results.</p>';
        });
    }
    private async submit(form: HTMLFormElement) { await this.guard(async () => { const d = new FormData(form); const get = (name: string) => String(d.get(name) || ''); if (form.dataset.form === 'connection') {
        await this.api().createConnection(get('name'), get('secret'), get('teams').split(',').map(t => t.trim()).filter(Boolean));
        const list = await this.api().list('connections');
        this.connections = list.items;
        this.render();
        return;
    } if (!this.selected)
        throw new Error('Select a connection first.'); const action = get('action'); const fields: Record<string, unknown> = { action, teamID: this.value('team'), issueID: get('issueID') }; if (get('title'))
        fields.title = get('title'); if (get('stateID'))
        fields.stateID = get('stateID'); if (get('text'))
        fields[action === 'addComment' ? 'body' : 'description'] = get('text'); const result = await this.api().operation(this.selected, fields); this.querySelector('[data-results]')!.textContent = action === 'comments' ? (result.nodes || []).map(n => String(n.body)).join('\n\n') || 'No comments.' : `Saved ${String(result.identifier || result.id || 'change')}.`; form.reset(); }); }
}
ensureFarosUIStyles();
if (!customElements.get('faros-provider-linear')) {
    const style = document.createElement('style');
    style.textContent = styles;
    document.head.appendChild(style);
    customElements.define('faros-provider-linear', LinearElement);
}
