// Hash routing for the embedded micro-frontend. The portal is an iframe/element
// with no server routes of its own, so navigation state lives in location.hash
// (never sent to the host). The shell mirrors the current Route to the hash on
// every render via replaceState (which does NOT fire hashchange → no loop) and
// restores it on load and on browser back/forward.
//
// Scheme:
//   #/agents                         Agents menu (default)
//   #/agents/<name>/chat|flow|settings   agent detail
//   #/connections #/toolsets #/schedules #/triggers #/models #/inbox

export type MenuKey = 'agents' | 'connections' | 'toolsets' | 'schedules' | 'triggers' | 'models' | 'inbox'
export type AgentTab = 'chat' | 'flow' | 'settings'

export const MENUS: MenuKey[] = ['agents', 'connections', 'toolsets', 'schedules', 'triggers', 'models', 'inbox']
const AGENT_TABS: AgentTab[] = ['chat', 'flow', 'settings']

export type Route = { kind: 'menu'; menu: MenuKey } | { kind: 'agent'; name: string; tab: AgentTab }

export const DEFAULT_ROUTE: Route = { kind: 'menu', menu: 'agents' }

// parseHash turns the current location.hash into a Route, applying one-way
// redirects for the legacy scheme (#/, #/agent/<n>/<tab>).
export function parseHash(): Route {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  // Legacy: #/agent/<n>/<tab> — flow was the old default (overview → flow).
  if (parts[0] === 'agent' && parts[1]) {
    return { kind: 'agent', name: decodeURIComponent(parts[1]), tab: normalizeLegacyTab(parts[2]) }
  }
  // New: #/agents/<n>/<tab>
  if (parts[0] === 'agents' && parts[1]) {
    return { kind: 'agent', name: decodeURIComponent(parts[1]), tab: normalizeTab(parts[2]) }
  }
  if (parts[0] && (MENUS as string[]).includes(parts[0])) {
    return { kind: 'menu', menu: parts[0] as MenuKey }
  }
  return DEFAULT_ROUTE
}

export function hashFor(route: Route): string {
  if (route.kind === 'agent') return `#/agents/${encodeURIComponent(route.name)}/${route.tab}`
  return `#/${route.menu}`
}

// syncHash mirrors the route to the URL without triggering a hashchange event.
export function syncHash(route: Route): void {
  const h = hashFor(route)
  if (location.hash !== h) {
    try {
      history.replaceState(null, '', h)
    } catch {
      /* sandboxed iframe without same-origin history — ignore */
    }
  }
}

function normalizeTab(t: string | undefined): AgentTab {
  return AGENT_TABS.includes(t as AgentTab) ? (t as AgentTab) : 'chat'
}
function normalizeLegacyTab(t: string | undefined): AgentTab {
  if (t === 'settings') return 'settings'
  if (t === 'chat') return 'chat'
  // overview (old read-only tab) and the old flow default both land on flow.
  return 'flow'
}
