// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Product telemetry: which page was opened and which marked control was
// clicked. Opt-in by the operator and off by default; docs/telemetry.md is
// the contract for what leaves the browser.
//
// This file is the vendor-neutral core and has no imports, so it can be
// tested on its own. install.ts connects it to the router, the document and
// the hub. Nothing here knows which analytics backend the hub relays to.
//
// What an event may carry is deliberately narrow, because the portal's URLs
// hold organization and workspace IDs and its pages hold tenant data:
//   - the route NAME and the route PATTERN (/:orgID/:workspaceID/mcp), never
//     the address bar;
//   - an event name and data-track-* labels an engineer wrote into the markup,
//     never text read from the page or a form.

export interface TelemetryEvent {
  type: 'page' | 'event'
  name: string
  route: string
  props?: Record<string, string>
  referrer?: string
  screen?: string
  language?: string
}

// The parts of a vue-router location this module reads.
export interface RouteLike {
  name?: string | symbol | null
  params?: Record<string, string | string[] | undefined>
  matched?: ReadonlyArray<{ path: string }>
}

const PROVIDER_NAME = /^[a-z0-9][a-z0-9-]{0,62}$/

// routePattern returns the pattern the route matched with its parameters left
// as placeholders: /:orgID/:workspaceID/settings. The one substitution is the
// provider page, whose pattern is the same for every provider; there the
// in-provider path is dropped and :name is replaced by the provider's name
// when it is a platform provider. An organization's own provider is named by
// that organization, so it is reported as "org-provider"; a name the catalog
// does not know yet stays the :name placeholder.
export type ProviderKind = 'platform' | 'org' | 'unknown'

export function routePattern(route: RouteLike, providerKind: (name: string) => ProviderKind = () => 'unknown'): string {
  let pattern = route.matched?.[route.matched.length - 1]?.path ?? ''
  // Strip the custom regexes and repeat modifiers vue-router allows in a
  // pattern: /:orgID([0-9a-f-]{36}) -> /:orgID, /:rest(.*)* -> /:rest.
  for (let previous = ''; previous !== pattern;) {
    previous = pattern
    pattern = pattern.replace(/\([^()]*\)/g, '')
  }
  pattern = pattern.replace(/[*+?]/g, '')
  if (route.name === 'provider-frame') {
    const name = route.params?.name
    pattern = pattern.replace('/:rest', '')
    const kind = typeof name === 'string' && PROVIDER_NAME.test(name) ? providerKind(name) : 'unknown'
    if (kind === 'platform') pattern = pattern.replace(':name', name as string)
    else if (kind === 'org') pattern = pattern.replace(':name', 'org-provider')
  }
  return pattern.startsWith('/') ? pattern : `/${pattern}`
}

export function pageEvent(route: RouteLike, providerKind?: (name: string) => ProviderKind): TelemetryEvent {
  return { type: 'page', name: typeof route.name === 'string' && route.name ? route.name : 'unnamed', route: routePattern(route, providerKind) }
}

// repeatsPage reports a provider page view that says nothing new. Every
// navigation inside a provider is a route change in the host, and all of them
// collapse to the same pattern once the in-provider path is dropped, so
// without this a provider page is counted once per click inside it.
export function repeatsPage(previous: TelemetryEvent | undefined, next: TelemetryEvent): boolean {
  return next.name === 'provider-frame' && previous?.name === 'provider-frame' && previous.route === next.route
}

// What may be reported before anyone has signed in: the sign-in page's own
// page views and its sign-in click. The hub enforces the same list on the
// route that takes no credentials; this copy only avoids sending what it
// would drop.
const PUBLIC_PAGES: Record<string, string> = { login: '/login', 'auth-callback': '/auth/callback' }

export function allowedBeforeSignIn(event: TelemetryEvent): boolean {
  if (event.type === 'page') return PUBLIC_PAGES[event.name] === event.route
  return event.name === 'sign_in' && event.route === PUBLIC_PAGES.login
}

// providerOfTag returns the provider whose UI a custom element is:
// RAILGRID-PROVIDER-EDGES -> edges, anything else -> ''.
export function providerOfTag(tagName: string): string {
  const match = /^railgrid-provider-([a-z0-9][a-z0-9-]{0,62})$/.exec(tagName.toLowerCase())
  return match ? match[1] : ''
}

// providerEvent decides what a click or a call from inside a provider's UI
// may report. A platform provider's bundle is ours, so its event passes and is
// labelled with the provider. An organization's own provider is code that
// organization wrote: its event names and labels are whatever it chose, so
// nothing from inside it is reported at all.
export function providerEvent(provider: string, kind: ProviderKind, props?: Record<string, string>): Record<string, string> | null {
  if (kind !== 'platform') return null
  return { ...props, provider }
}

// trackProps reads data-track-placement="sidebar" as { placement: 'sidebar' }.
export function trackProps(dataset: Record<string, string | undefined>): Record<string, string> {
  const props: Record<string, string> = {}
  for (const [key, value] of Object.entries(dataset)) {
    if (key.length > 5 && key.startsWith('track') && value) props[key[5].toLowerCase() + key.slice(6)] = value
  }
  return props
}

// externalOrigin returns the origin of a referrer on another site, and
// nothing for an in-app one (whose path would carry tenant IDs).
export function externalOrigin(referrer: string, ownOrigin: string): string {
  try {
    const origin = new URL(referrer).origin
    return origin !== ownOrigin && origin !== 'null' ? origin : ''
  } catch {
    return ''
  }
}

export interface TrackerOptions {
  // Whether the hub has telemetry turned on. Read on every event.
  enabled: () => boolean
  // Delivers one batch. Must not throw into the caller's flow.
  send: (events: TelemetryEvent[]) => void
  // Milliseconds events wait so a burst leaves as one request.
  delay?: number
  schedule?: (flush: () => void, delay: number) => void
}

// Matches the hub relay's per-request limit.
export const MAX_BATCH = 20

export function createTracker(options: TrackerOptions) {
  const schedule = options.schedule ?? ((flush, delay) => { setTimeout(flush, delay) })
  let queue: TelemetryEvent[] = []
  let scheduled = false

  function flush(): void {
    scheduled = false
    while (queue.length) {
      const batch = queue.slice(0, MAX_BATCH)
      queue = queue.slice(MAX_BATCH)
      try { options.send(batch) } catch { /* telemetry never affects the app */ }
    }
  }

  function record(event: TelemetryEvent): void {
    if (!options.enabled()) return
    // An unsent backlog means the hub is unreachable; do not grow it.
    if (queue.length >= MAX_BATCH * 5) return
    queue.push(event)
    if (scheduled) return
    scheduled = true
    schedule(flush, options.delay ?? 1000)
  }

  return { record, flush }
}
