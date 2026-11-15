---
{"schema":1,"id":"design.journeys.sign-in-and-session-recovery","title":"Sign-in and session recovery","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"The host portal implements these paths; identity-provider screens and product-wide session behavior are outside this entry's verification."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/sign-in-and-session-recovery.md#sign-in-and-session-recovery","role":"design"},{"path":"portal/src/pages/LoginPage.vue","role":"implementation"},{"path":"portal/src/pages/AuthCallback.vue","role":"implementation"},{"path":"portal/src/auth/session.ts","role":"implementation"},{"path":"portal/src/router/contextGuard.ts","role":"implementation"},{"path":"portal/src/stores/auth.ts","role":"implementation"},{"path":"portal/src/App.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of portal sign-in and session recovery","status":"passing","evidence":"Source behavior reviewed; a live identity provider, browser interaction, and assistive technology have not been checked in this review."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"related"},{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.content.ui-copy","relation":"see-also"}]}
---

# Sign-in and session recovery

This journey describes the host portal's current behavior. An identity provider
may present its own sign-in, error, and recovery screens; this entry does not
specify those screens or promise preservation of unsaved work.

## Enter and authenticate

A protected portal destination requires a session. When no bearer is available,
the route guard sends the user to sign-in and remembers the intended portal
destination for the current tab. The login page discovers the hub's available
methods: SSO, bearer token, or both. When both are available, SSO is prominent
and a separate action reveals the token form. If discovery fails, the current
implementation presents the token form. The token field is masked and labeled;
submission is disabled while empty or signing in. A failed token submission
shows an assertive error associated with the field and leaves the user on the
form to retry.

SSO leaves the portal for authorization. The callback displays a polite busy
status while completing sign-in. If the callback cannot be processed, it shows
an assertive error and a link back to login. The identity provider owns its own
credential and account recovery experience.

## Resume the destination

After successful sign-in, the portal resumes a saved app-authorization
continuation or portal destination, falling back to the portal root. Remembering
either valid continuation clears the other; if a login URL supplies both, its
valid `returnTo` portal destination is remembered last. App continuations are
restricted to the hub's two app authorization routes, while portal destinations
must be local and cannot point back to login or auth. Both are single-use per
tab. On the root route, the portal rechecks organization and
workspace access before resuming a remembered scope or presenting organization
selection. Successful sign-in does not guarantee access to the original resource.

## Recover a session

The portal attempts to refresh an expired bearer. An unrefreshable bearer or a
401 from authenticated `authFetch` signals a dead session; the shell clears the
session and returns to login with the current destination. Multiple concurrent
failures are latched to one expiration event until the next successful sign-in.
A 403 or 404 alone does not trigger this session-expiration path. This describes
`authFetch` and the host shell, not every request made by every provider.

After a browser refresh, a retained bearer may need to restore the separate
browser session. Protected routes wait for this initialization before mounting.
A failed browser-session bootstrap does not by itself prevent the bearer-backed
portal from continuing. For an app continuation, however, login reports that the
browser session could not be restored and asks the user to sign in again.

## Evidence and boundaries

The canonical paths are the [login page](../../../portal/src/pages/LoginPage.vue),
[callback](../../../portal/src/pages/AuthCallback.vue),
[session handling](../../../portal/src/auth/session.ts),
[auth store](../../../portal/src/stores/auth.ts),
[route guard](../../../portal/src/router/contextGuard.ts), and
[host shell](../../../portal/src/App.vue). The
[scoped navigation tests](../../../portal/src/router/scopedNavigation.test.mjs)
exercise continuation and route selection with simulated state. They do not
establish live identity-provider, screen-reader, or all-provider behavior. This
entry records source review only; no browser or live authentication check was
performed for it.

For visual context, the [Dex login specimen](http://127.0.0.1:5197/dex.html)
shows an identity-provider screen with sign-in disabled; it does not exercise
the host login or session recovery. Start the local server using the
[design review setup](../../../hack/design-review/README.md).
