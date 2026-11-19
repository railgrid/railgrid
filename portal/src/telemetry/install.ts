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
import { createTracker, externalOrigin, pageEvent, routePattern, trackProps, type ProviderKind, type TelemetryEvent } from './telemetry'

const EVENTS_PATH = '/api/telemetry/events'
// Where the user came from, kept for the tab's lifetime: the sign-in redirect
// reloads the app, and by then document.referrer names the identity provider.
const REFERRER_KEY = 'railgrid:telemetry:referrer'

let track: (name: string, props?: Record<string, string>) => void = () => {}

// trackEvent records a named interaction from host code. It does nothing
// unless the operator enabled telemetry. Markup should prefer the declarative
// form: <button data-track="enable_provider" data-track-placement="card">.
export function trackEvent(name: string, props?: Record<string, string>): void {
  track(name, props)
}

// installTelemetry connects the tracker to the router and the document. It is
// inert until the hub's /healthz says telemetry is on, which a hub without
// --telemetry-url never does: no request, no storage, nothing recorded.
//
// Providers need no code for clicks: their UIs render in this document, so a
// data-track attribute inside a provider element is seen by the same listener.
export function installTelemetry(router: Router): void {
  const arrivedFrom = externalOrigin(document.referrer, window.location.origin)
  const enabled = () => {
    const auth = useAuthStore()
    // The relay only accepts signed-in callers; before sign-in there is
    // nothing to send and no one to send it as.
    return auth.healthz?.telemetry === true && !!auth.token
  }

  function send(events: TelemetryEvent[]): void {
    const token = useAuthStore().token
    if (!token) return
    // Plain fetch on purpose: a rejected telemetry call must not trip the
    // session-expired handling that authFetch applies to real API calls.
    void fetch(EVENTS_PATH, {
      method: 'POST',
      keepalive: true,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify({ events }),
    }).catch(() => { /* best effort */ })
  }

  const tracker = createTracker({ enabled, send })
  // Platform provider names are ours and safe to report; an organization's
  // own provider is named by that organization and is not.
  const providerKind = (name: string): ProviderKind => {
    const provider = useProvidersStore().items.find((item) => item.name === name)
    return provider ? (provider.scope === 'global' ? 'platform' : 'org') : 'unknown'
  }
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
  router.afterEach((to, _from, failure) => {
    if (failure) return
    if (useAuthStore().healthz?.telemetry === true) landingReferrer()
    if (!enabled()) return
    const event: TelemetryEvent = { ...pageEvent(to, providerKind), ...context() }
    if (firstPage) {
      firstPage = false
      const referrer = landingReferrer()
      if (referrer) event.referrer = referrer
    }
    tracker.record(event)
  })

  track = (name, props) => {
    tracker.record({ type: 'event', name, route: routePattern(router.currentRoute.value, providerKind), props, ...context() })
  }

  function onClick(event: MouseEvent): void {
    // A middle click opens a link too; other auxiliary buttons do not.
    if (event.type === 'auxclick' && event.button !== 1) return
    const element = (event.target as Element | null)?.closest?.<HTMLElement>('[data-track]')
    if (element?.dataset.track) track(element.dataset.track, trackProps(element.dataset))
  }
  document.addEventListener('click', onClick, true)
  document.addEventListener('auxclick', onClick, true)

  // Deliver what is queued before the tab goes away.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') tracker.flush()
  })
}
