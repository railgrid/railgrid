// AppStore holds the workspace's entity collections and the loaders that fetch
// them. It is the single source of truth the views read from. A load method
// refreshes one collection then calls onChange() so the shell re-renders — the
// same "reload then re-render" contract the monolithic element used.

import type { ApiClient } from './api'
import type { Agent, Connection, Credential, InboxItem, Schedule, Toolset, Trigger } from './types'
import { CONN_CATEGORY, familiesForConns } from './conn-defs'

export class AppStore {
  agents: Agent[] = []
  schedules: Schedule[] = []
  triggers: Trigger[] = []
  connections: Connection[] = []
  toolsets: Toolset[] = []
  credentials: Credential[] = []
  inbox: InboxItem[] = []
  oauthApps = new Set<string>()
  error: string | null = null

  private api: ApiClient
  private onChange: () => void

  constructor(api: ApiClient, onChange: () => void) {
    this.api = api
    this.onChange = onChange
  }

  // loadAll fires every collection loader. Each resolves independently and
  // re-renders as it lands, so the UI fills in progressively.
  loadAll(): void {
    void this.loadAgents()
    void this.loadCredentials()
    void this.loadConnections()
    void this.loadToolsets()
    void this.loadSchedules()
    void this.loadTriggers()
    void this.loadInbox()
    void this.loadOAuthApps()
  }

  agent(name: string | null): Agent | undefined {
    return this.agents.find((a) => a.metadata.name === name)
  }
  connectionType(name: string): string | undefined {
    return this.connections.find((c) => c.metadata.name === name)?.spec.type
  }
  // families derived from a list of tool-connection names (never hand-picked).
  familiesFor(names: string[]): string[] {
    return familiesForConns(names, (n) => this.connectionType(n))
  }
  toolConnections(): Connection[] {
    return this.connections.filter((c) => CONN_CATEGORY[c.spec.type] === 'tool')
  }

  async loadAgents(): Promise<void> {
    if (!this.api.hasWorkspace()) return this.onChange()
    try {
      this.agents = await this.api.list<Agent>('/api/agents')
      this.error = null
    } catch (e) {
      this.error = 'Failed to load agents: ' + (e as Error).message
    }
    this.onChange()
  }
  async loadSchedules(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.schedules = await this.api.list<Schedule>('/api/schedules')
    } catch {
      /* view shows its own empty state */
    }
    this.onChange()
  }
  async loadTriggers(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.triggers = await this.api.list<Trigger>('/api/triggers')
    } catch {
      /* non-fatal */
    }
    this.onChange()
  }
  async loadConnections(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.connections = await this.api.list<Connection>('/api/connections')
    } catch {
      /* non-fatal */
    }
    this.onChange()
  }
  async loadToolsets(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.toolsets = await this.api.list<Toolset>('/api/toolsets')
    } catch {
      /* backend may predate toolsets — non-fatal */
    }
    this.onChange()
  }
  async loadCredentials(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.credentials = await this.api.list<Credential>('/api/credentials')
      this.onChange()
    } catch {
      /* models view can still create the first credential */
    }
  }
  async loadInbox(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      this.inbox = await this.api.list<InboxItem>('/api/inbox')
      this.onChange()
    } catch {
      /* non-fatal */
    }
  }
  async loadOAuthApps(): Promise<void> {
    if (!this.api.hasWorkspace()) return
    try {
      const res = await this.api.get<{ providers?: Record<string, boolean> }>('/api/oauth/providers')
      const next = new Set(Object.entries(res.providers || {}).filter(([, v]) => v).map(([k]) => k))
      const changed = next.size !== this.oauthApps.size || [...next].some((p) => !this.oauthApps.has(p))
      this.oauthApps = next
      // Re-render so an already-open connection form drops the client id/secret
      // fields now that we know a platform app exists (the fetch is async).
      if (changed) this.onChange()
    } catch {
      /* optional — falls back to BYO client id/secret */
    }
  }
}
