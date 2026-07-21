// ViewCtx is what the shell hands every view: the data store, the API client,
// the current route, and callbacks to navigate / surface a note / re-render.
// Views are pure-ish: render(vc) returns HTML, wire(vc, root) attaches
// listeners. Mutations go through the shared helpers in actions.ts.

import type { ApiClient } from './api'
import type { AppStore } from './store'
import type { Route } from './router'

export interface ViewCtx {
  store: AppStore
  api: ApiClient
  route: Route
  navigate(route: Route): void
  // notify sets the dismissible note bar at the top of the shell (used for both
  // success and failure messages, matching the original element).
  notify(msg: string | null): void
  rerender(): void
}
