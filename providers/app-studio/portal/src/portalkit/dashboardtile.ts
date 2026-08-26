// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Shared, framework-agnostic scaffolding for a provider's dashboard tile — the
// <faros-dashboard-tile-{name}> element the console mounts on its dashboard
// page. Rendering stays with each provider (its own resources, its own words);
// what lives here is the plumbing every tile was otherwise going to re-derive:
//
//   - the poll loop and its cadence
//   - "no workspace selected yet" and "provider not bound yet" as EMPTY, not
//     as an error banner — a tile is glanceable chrome, and a red box for a
//     workspace that simply has not been bootstrapped is noise
//   - the faros-navigate dispatch that turns a row into a console route
//   - the recency sort + cap that keeps every tile the same height
//
// Deliberately plain TypeScript, like the rest of portalkit: it is synced into
// vanilla-TS and Vue portals alike, so it must not import a framework.

// tileClass is the ONE visual vocabulary every dashboard tile renders with.
//
// Tiles are cards sitting side by side in one grid, so a tile that invents its
// own row padding, border or type scale reads as a different product. These
// were previously inline in the infrastructure tile — the first one written —
// and every tile added afterwards approximated them by eye. Naming them here
// means a change lands everywhere at once.
//
// Tailwind scans this file (it is vendored into each portal's src/), so the
// literal strings still generate their utilities.
export const tileClass = {
  // Vertical rhythm of the whole tile body.
  root: 'space-y-3',
  // The headline row: counts and status chips, wrapping on narrow cards.
  stats: 'flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]',
  // One chip inside that row. Every chip is glyph + number + word, in that
  // order — a row where some chips carry an icon and others do not reads as
  // two different components sharing a card.
  stat: 'inline-flex items-center gap-1',
  statIcon: 'h-3 w-3 shrink-0',
  // Tones. `statTotal` is the neutral headline count; the rest carry meaning,
  // so a tile should reach for them only when the number is actionable.
  statTotal: 'text-text-primary',
  statOk: 'text-success',
  statWarn: 'text-warning',
  statBad: 'text-danger',
  statMuted: 'text-text-muted',
  // The number itself: tabular so counts do not jitter as they poll.
  statNum: 'font-semibold tabular-nums',
  statLabel: 'text-text-muted',
  // Small caps heading above a list ("Recent", "Offline first", …).
  sectionLabel: 'mb-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted',
  list: 'space-y-1',
  // One clickable row. `group` drives the chevron's hover motion.
  row: 'group flex w-full items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/40 px-2.5 py-1.5 text-left transition-colors hover:bg-accent/[0.04]',
  // Primary identifier — takes the slack and truncates.
  rowPrimary: 'min-w-0 flex-1 truncate text-[12px] text-text-primary',
  // Secondary fact (phase, age, template) — never truncates the primary away.
  rowSecondary: 'shrink-0 truncate text-[10px] text-text-muted/70',
  // Leading status indicator on a row. One dot, one meaning, same size on
  // every tile — rows that lead with a full icon on one card and a dot on the
  // next read as two different lists.
  rowDot: 'h-1.5 w-1.5 shrink-0 rounded-full',
  chevron: 'h-3 w-3 shrink-0 text-text-muted/30 transition-all group-hover:translate-x-0.5 group-hover:text-accent/60',
  // Empty state: dashed, so it reads as "nothing here" rather than a row.
  empty: 'rounded-lg border border-dashed border-border-subtle p-3 text-center text-[11px] text-text-muted',
  message: 'text-[11px] text-text-muted',
  error: 'text-[11px] text-danger',
} as const

// TileContext is the subset of farosContext a tile needs. The console pushes
// the full object; tiles only ever read these.
export interface TileContext {
  token?: string | null
  tenant?: string | null
  // Hub-proxy providers need the persisted tenant selection as well as the
  // resolved kcp tenant path so their requests carry the same headers as the
  // full provider surface.
  orgUUID?: string | null
  workspaceUUID?: string | null
  basePath?: string
}

// Note there is no `theme` here, though the console pushes one. A tile renders
// inside the console's own DOM and inherits its CSS variables, so branching on
// the theme is how two cards end up disagreeing about what dark means. Tiles
// that need a colour take it from the shared palette classes.

// TILE_POLL_MS matches the console's own list cadence. Anything tighter spends
// hub round trips on a card users glance at.
export const TILE_POLL_MS = 30000

// TILE_ROWS is the row cap every tile shares so the dashboard grid stays even.
export const TILE_ROWS = 4

// benignReasons are the API reasons that mean "nothing here yet" rather than
// "something is broken": no workspace selected, or the provider's APIs are not
// bound in this workspace. Both are ordinary states for a fresh tenant.
const benignReasons = new Set([
  'TenantMissing',
  'APIBindingMissing',
  'NotFound',
])

export interface TileError {
  reason?: string
  message?: string
}

// isBenignTileError reports whether a failed load should render as an empty
// tile. Callers that get true must clear their error state, not set it.
export function isBenignTileError(err: unknown): boolean {
  if (!err) return true
  const reason = (err as TileError).reason
  if (reason && benignReasons.has(reason)) return true
  const message = ((err as TileError).message ?? String(err)).toLowerCase()
  // The hub answers a not-yet-bootstrapped workspace with a 404 whose body
  // names the missing resource rather than a typed reason.
  return message.includes('server could not find the requested resource')
    || message.includes('no workspace selected')
}

// tileErrorText renders a failure for the tile's one-line error slot.
export function tileErrorText(err: unknown): string {
  const reason = (err as TileError).reason
  const message = (err as TileError).message ?? String(err)
  return reason ? `${reason} — ${message}` : message
}

// mostRecent sorts by an ISO timestamp accessor, newest first, and caps the
// list. Undated items sort last rather than being dropped — a row with no
// timestamp is still a row the user may need to click.
export function mostRecent<T>(items: T[], at: (item: T) => string | undefined, limit = TILE_ROWS): T[] {
  return [...items]
    .sort((a, b) => (at(b) || '').localeCompare(at(a) || ''))
    .slice(0, limit)
}

// countBy tallies items by a key accessor — the breakdown row every tile shows
// under its headline.
export function countBy<T>(items: T[], key: (item: T) => string): Record<string, number> {
  const out: Record<string, number> = {}
  for (const item of items) {
    const k = key(item)
    if (!k) continue
    out[k] = (out[k] ?? 0) + 1
  }
  return out
}

// navigateFromTile bubbles a console route request out of the tile. The
// console's DashboardTile listener turns it into
// router.push('/providers/{name}/' + path), so tiles never import a router.
export function navigateFromTile(el: Element | null | undefined, path: string): void {
  el?.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
}

export interface TilePoller {
  // start runs load immediately and then on the interval.
  start(): void
  stop(): void
  // refresh runs load once, out of band — for a context change.
  refresh(): void
}

// createTilePoller owns the load-now-then-poll lifecycle, including the
// overlap guard: a slow load must not stack up behind the interval, which is
// how a tile against a struggling backend turns into a request flood.
//
// A refresh that arrives mid-load is COALESCED, not dropped. Dropping it looks
// harmless until you notice the sequence every tile actually starts with: the
// element is appended (poller starts, loads with no context yet, renders
// empty), and the console pushes farosContext in the very next statement. That
// refresh lands while the first load is still settling, so discarding it left
// the tile showing its empty state until the next interval — or forever, if
// the context never changes again.
export function createTilePoller(load: () => Promise<void>, intervalMs = TILE_POLL_MS): TilePoller {
  let handle: ReturnType<typeof setInterval> | null = null
  let inFlight = false
  let queued = false
  let stopped = false

  const run = () => {
    if (stopped) return
    if (inFlight) {
      queued = true
      return
    }
    inFlight = true
    void load().finally(() => {
      inFlight = false
      if (queued && !stopped) {
        queued = false
        run()
      }
    })
  }

  return {
    start() {
      stopped = false
      run()
      if (handle === null) handle = setInterval(run, intervalMs)
    },
    stop() {
      stopped = true
      queued = false
      if (handle !== null) {
        clearInterval(handle)
        handle = null
      }
    },
    refresh: run,
  }
}

// hasWorkspaceContext reports whether the console has pushed a workspace yet.
// Loading before it has produces a guaranteed failure, so tiles check first and
// render empty instead.
export function hasWorkspaceContext(ctx: TileContext | null | undefined): boolean {
  return !!ctx && !!ctx.tenant
}
