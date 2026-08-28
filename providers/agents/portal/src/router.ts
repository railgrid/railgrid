// Hash routing for the embedded micro-frontend. The element has no server
// routes of its own, so navigation state lives in location.hash (never sent to
// the host). The shell mirrors user navigation to the hash with pushState and
// restores routes on load, host hash assignments, and browser back/forward.
//
// Scheme:
//   #/agents                          Agents grid (default)
//   #/agents/<name>/config|runs       agent detail
//   #/activity                        run feed + approvals
//   #/activity/<runID>                run trace
//   #/connections #/models
//   #/create/agent
//   #/create/connection[/<type>]
//   #/create/toolset #/create/model

export type MenuKey = 'agents' | 'activity' | 'connections' | 'models'
export type AgentTab = 'config' | 'runs'
export type CreateResource = 'agent' | 'connection' | 'toolset' | 'model'

export const MENUS: MenuKey[] = ['agents', 'activity', 'connections', 'models']
const AGENT_TABS: AgentTab[] = ['config', 'runs']

export type Route =
  | { kind: 'menu'; menu: MenuKey }
  | { kind: 'agent'; name: string; tab: AgentTab }
  | { kind: 'run'; id: string }
  | { kind: 'create'; resource: CreateResource; type?: string }

// Create surfaces send this event after their API write succeeds. Keeping the
// result on the event lets the shell make it immediately visible in the owning
// collection while its authoritative reload is still in flight.
export interface CreateSuccessDetail {
  resource: CreateResource
  name?: string
  item?: unknown
  destination?: Route
}

export const DEFAULT_ROUTE: Route = { kind: 'menu', menu: 'agents' }

// parseHash turns the current location.hash into a Route. Routes from the old
// 7-tab scheme (inbox / schedules / triggers / toolsets, agent flow+settings
// tabs) redirect one-way onto their new home.
export function parseHash(hash = location.hash): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  const [head, second, third] = parts
  if (head === 'create') {
    if (second === 'agent' || second === 'toolset' || second === 'model') return { kind: 'create', resource: second }
    if (second === 'connection') {
      return { kind: 'create', resource: 'connection', ...(third ? { type: decodePart(third) } : {}) }
    }
  }
  if ((head === 'agents' || head === 'agent') && second) {
    return { kind: 'agent', name: decodePart(second), tab: normalizeTab(third) }
  }
  if (head === 'activity' && second) return { kind: 'run', id: decodePart(second) }
  if (head === 'runs' && second) return { kind: 'run', id: decodePart(second) }
  if ((MENUS as string[]).includes(head)) return { kind: 'menu', menu: head as MenuKey }
  // Legacy tabs fold into their absorbing surface.
  if (head === 'inbox' || head === 'runs') return { kind: 'menu', menu: 'activity' }
  if (head === 'toolsets') return { kind: 'menu', menu: 'connections' }
  if (head === 'schedules' || head === 'triggers') return { kind: 'menu', menu: 'agents' }
  return DEFAULT_ROUTE
}

export function hashFor(route: Route): string {
  switch (route.kind) {
    case 'agent':
      return `#/agents/${encodeURIComponent(route.name)}/${route.tab}`
    case 'run':
      return `#/activity/${encodeURIComponent(route.id)}`
    case 'create':
      return `#/create/${route.resource}${route.type ? `/${encodeURIComponent(route.type)}` : ''}`
    default:
      return `#/${route.menu}`
  }
}

export type HashHistoryMode = 'push' | 'replace'

// writeHash mirrors the route to the URL. pushState deliberately does not fire
// hashchange, so the caller updates its in-memory route at the same time;
// popstate and hashchange listeners in the shell cover browser traversal and
// host-side hash assignments respectively.
export function writeHash(route: Route, mode: HashHistoryMode = 'push'): void {
  const h = hashFor(route)
  if (location.hash !== h) {
    try {
      if (mode === 'replace') history.replaceState(null, '', h)
      else history.pushState(null, '', h)
    } catch {
      // Sandboxed history — assignment still gives the host a usable route.
      location.hash = h
    }
  }
}

// syncHash is retained for callers that need to canonicalize an initial or
// externally supplied hash without adding a history entry.
export function syncHash(route: Route): void {
  writeHash(route, 'replace')
}

// activeMenu is which nav tab lights up for a route (detail pages keep their
// parent tab highlighted).
export function activeMenu(route: Route): MenuKey {
  if (route.kind === 'agent') return 'agents'
  if (route.kind === 'run') return 'activity'
  if (route.kind === 'create') {
    if (route.resource === 'model') return 'models'
    if (route.resource === 'connection' || route.resource === 'toolset') return 'connections'
    return 'agents'
  }
  return route.menu
}

function decodePart(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    // A malformed external hash should not break the entire embedded portal.
    return value
  }
}

function normalizeTab(t: string | undefined): AgentTab {
  if (AGENT_TABS.includes(t as AgentTab)) return t as AgentTab
  // flow / wiring / settings / chat all lived in what is now one Config pane.
  return 'config'
}
