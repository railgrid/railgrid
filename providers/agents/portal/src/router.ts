// Hash routing for the embedded micro-frontend. The element has no server
// routes of its own, so navigation state lives in location.hash (never sent to
// the host). The shell mirrors the current Route to the hash on every render via
// replaceState (which does NOT fire hashchange → no loop) and restores it on
// load and on browser back/forward.
//
// Scheme:
//   #/agents                          Agents grid (default)
//   #/agents/<name>/config|runs       agent detail
//   #/activity                        run feed + approvals
//   #/activity/<runID>                run trace
//   #/connections #/models

export type MenuKey = 'agents' | 'activity' | 'connections' | 'models'
export type AgentTab = 'config' | 'runs'

export const MENUS: MenuKey[] = ['agents', 'activity', 'connections', 'models']
const AGENT_TABS: AgentTab[] = ['config', 'runs']

export type Route =
  | { kind: 'menu'; menu: MenuKey }
  | { kind: 'agent'; name: string; tab: AgentTab }
  | { kind: 'run'; id: string }

export const DEFAULT_ROUTE: Route = { kind: 'menu', menu: 'agents' }

// parseHash turns the current location.hash into a Route. Routes from the old
// 7-tab scheme (inbox / schedules / triggers / toolsets, agent flow+settings
// tabs) redirect one-way onto their new home.
export function parseHash(hash = location.hash): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  const [head, second, third] = parts
  if ((head === 'agents' || head === 'agent') && second) {
    return { kind: 'agent', name: decodeURIComponent(second), tab: normalizeTab(third) }
  }
  if (head === 'activity' && second) return { kind: 'run', id: decodeURIComponent(second) }
  if (head === 'runs' && second) return { kind: 'run', id: decodeURIComponent(second) }
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
    default:
      return `#/${route.menu}`
  }
}

// syncHash mirrors the route to the URL without triggering a hashchange event.
export function syncHash(route: Route): void {
  const h = hashFor(route)
  if (location.hash !== h) {
    try {
      history.replaceState(null, '', h)
    } catch {
      /* sandboxed history — the route just won't survive a refresh */
    }
  }
}

// activeMenu is which nav tab lights up for a route (detail pages keep their
// parent tab highlighted).
export function activeMenu(route: Route): MenuKey {
  if (route.kind === 'agent') return 'agents'
  if (route.kind === 'run') return 'activity'
  return route.menu
}

function normalizeTab(t: string | undefined): AgentTab {
  if (AGENT_TABS.includes(t as AgentTab)) return t as AgentTab
  // flow / wiring / settings / chat all lived in what is now one Config pane.
  return 'config'
}
