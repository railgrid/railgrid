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

import type { Router } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useProvidersStore } from '@/stores/providers'
import { allowedBeforeSignIn, createTracker, externalOrigin, pageEvent, providerEvent, providerOfTag, repeatsPage, routePattern, trackProps, type ProviderKind, type TelemetryEvent } from './telemetry'
import { setTrack, trackEvent } from './track'

const EVENTS_PATH = '/api/telemetry/events'
// The sign-in page reports here, without credentials. The hub accepts only
// that page's own events on it.
const PUBLIC_EVENTS_PATH = '/api/telemetry/public'
// Where the user came from, kept for the tab's lifetime: the sign-in redirect
// reloads the app, and by then document.referrer names the identity provider.
const REFERRER_KEY = 'railgrid:telemetry:referrer'

// trackEvent records an outcome or a flow step from host code; see track.ts.
// For a click, prefer the declarative form in the markup:
// <button data-track="enable_provider" data-track-placement="card">.
export { trackEvent }

let providerKindOf: (name: string) => ProviderKind = () => 'unknown'

// providerTracker is the `track` function the host hands a provider's UI on
// railgridContext, for outcomes that are not clicks. Events from a platform
// provider are labelled with its name; an organization's own provider gets a
// function that records nothing.
export function providerTracker(provider: string): (name: string, props?: Record<string, string>) => void {
  return (name, props) => {
    const labelled = providerEvent(provider, providerKindOf(provider), props)
    if (labelled) trackEvent(name, labelled)
  }
}

// installTelemetry connects the tracker to the router and the document. It is
// inert until the hub's /healthz says telemetry is on, which a hub without
// --telemetry-url never does: no request, no storage, nothing recorded.
//
// Providers need no code for clicks: their UIs render in this document, so a
// data-track attribute inside a provider element is seen by the same listener.
export function installTelemetry(router: Router): void {
  const arrivedFrom = externalOrigin(document.referrer, window.location.origin)
  const enabled = () => useAuthStore().healthz?.telemetry === true

  function send(events: TelemetryEvent[]): void {
    const token = useAuthStore().token
    // Before sign-in only the sign-in page's own events leave, to the route
    // that takes no credentials.
    if (!token) events = events.filter(allowedBeforeSignIn)
    if (!events.length) return
    // Plain fetch on purpose: a rejected telemetry call must not trip the
    // session-expired handling that authFetch applies to real API calls.
    void fetch(token ? EVENTS_PATH : PUBLIC_EVENTS_PATH, {
      method: 'POST',
      keepalive: true,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify({ events }),
    }).catch(() => { /* best effort */ })
  }

  const tracker = createTracker({ enabled, send })
  // Signed out, anything but the sign-in page's own events is not recorded.
  const record = (event: TelemetryEvent) => {
    if (useAuthStore().token || allowedBeforeSignIn(event)) tracker.record(event)
  }
  // Platform provider names are ours and safe to report; an organization's
  // own provider is named by that organization and is not.
  const providerKind = (name: string): ProviderKind => {
    const provider = useProvidersStore().items.find((item) => item.name === name)
    return provider ? (provider.scope === 'global' ? 'platform' : 'org') : 'unknown'
  }
  providerKindOf = providerKind
  const context = () => ({
    screen: `${window.screen.width}x${window.screen.height}`,
    language: navigator.language,
  })

  // Remember the arrival origin once telemetry is known to be on, so it
  // survives the sign-in round trip. '' is stored too: a later load in this
  // tab would otherwise record the identity provider as the origin.
  function landingReferrer(): string {
    try {
      const stored = sessionStorage.getItem(REFERRER_KEY)
      if (stored !== null) return stored
      sessionStorage.setItem(REFERRER_KEY, arrivedFrom)
    } catch { /* storage unavailable: fall back to this load's referrer */ }
    return arrivedFrom
  }

  let firstPage = true
  let lastPage: TelemetryEvent | undefined
  router.afterEach((to, _from, failure) => {
    if (failure) return
    if (useAuthStore().healthz?.telemetry === true) landingReferrer()
    if (!enabled()) return
    const event: TelemetryEvent = { ...pageEvent(to, providerKind), ...context() }
    if (repeatsPage(lastPage, event)) return
    lastPage = event
    if (firstPage) {
      firstPage = false
      const referrer = landingReferrer()
      if (referrer) event.referrer = referrer
    }
    record(event)
  })

  const track = (name: string, props?: Record<string, string>) => {
    record({ type: 'event', name, route: routePattern(router.currentRoute.value, providerKind), props, ...context() })
  }
  setTrack(track)

  function onClick(event: MouseEvent): void {
    // A middle click opens a link too; other auxiliary buttons do not.
    if (event.type === 'auxclick' && event.button !== 1) return
    const element = (event.target as Element | null)?.closest?.<HTMLElement>('[data-track]')
    if (!element?.dataset.track) return
    let props: Record<string, string> | null = trackProps(element.dataset)
    // A control inside a provider's UI: see providerEvent for what may leave.
    let provider = ''
    for (let node: Element | null = element; node && !provider; node = node.parentElement) provider = providerOfTag(node.tagName)
    if (provider) props = providerEvent(provider, providerKind(provider), props)
    if (props) track(element.dataset.track, props)
  }
  document.addEventListener('click', onClick, true)
  document.addEventListener('auxclick', onClick, true)

  // Deliver what is queued before the tab goes away. pagehide covers a
  // navigation to another site (the identity provider, checkout), which is
  // exactly when the last click is the one worth having.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') tracker.flush()
  })
  window.addEventListener('pagehide', () => tracker.flush())
}
