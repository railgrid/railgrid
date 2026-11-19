---
layout: default
title: Product telemetry
description: "Opt-in console usage reporting to an analytics backend you run: off by default, what is sent and what never is"
---

# Product telemetry

Railgrid can report how the console is used (which pages are opened, which
marked controls are clicked) to an analytics backend **you run**. It is for
operators who want to see where users go and where they stop.

**It is off by default.** A hub started without `--telemetry-url`:

- registers no telemetry route (`POST /api/telemetry/events` answers 404),
- answers `"telemetry": false` on `/healthz`, which is the portal's only
  signal, so the portal records nothing, stores nothing and sends nothing,
- makes no outbound telemetry call.

Railgrid itself never phones home: there is no built-in destination. Events go
only to the URL the operator configures.

## Turning it on

The backend is [Umami](https://umami.is) (open source, self-hosted). Create a
website in it and give the hub its URL and website ID:

```yaml
# railgrid-hub chart values
hub:
  telemetry:
    url: https://telemetry.example.com
    websiteID: 6ac408ea-8b0d-4a6e-b7f1-5d369db5bb19
```

or `--telemetry-url` and `--telemetry-website-id` on the binary. Both are
required together; the hub refuses to start with one of them.

The portal then picks it up on its next load. No rebuild is needed: the
tracker ships in every portal bundle and stays inert until `/healthz` says
telemetry is on.

Set `hub.trustedProxyCIDRs` (`--trusted-proxy-cidrs`) if the hub sits behind
a proxy. Without it the hub sees every browser as the proxy's address, and the
backend then merges users who share a browser version into one session.

## How it flows

```
browser ── POST /api/telemetry/events ──▶ hub ── POST {url}/api/send ──▶ Umami
          (same origin, signed-in only)       (the hub's own outbound call)
```

The portal's Content-Security-Policy stays `connect-src 'self'`: the browser
talks only to the hub. The hub validates each event, answers `204` at once and
delivers from a bounded queue in the background. Delivery is best effort: when
the backend is slow or down, events are dropped; a request is never slowed and
nothing is retried.

Only signed-in callers can post, so the route cannot be used as an open proxy
to the backend, and nothing is recorded on the sign-in page.

## What is sent

| Field | Example | Notes |
| ----- | ------- | ----- |
| Route name | `mcp-detail` | the name in the portal's route table |
| Route pattern | `/:orgID/:workspaceID/mcp/:name` | parameters stay placeholders |
| Event name | `enable_provider` | from a `data-track` attribute in the markup |
| Event labels | `provider: agents` | from `data-track-*` attributes |
| Arrival origin | `https://railgrid.ai` | first page only; scheme and host, no path |
| Screen size, language | `1440x900`, `en-GB` | |
| Browser address and user agent | | passed to the backend, see below |

Platform provider pages are reported by provider name
(`/:orgID/:workspaceID/providers/agents`).

## What is never sent

- **The address bar.** Organization, workspace and resource IDs and names
  appear in portal URLs; only the route pattern leaves the browser.
- **Page titles, text on the page, form values.** Click events carry only what
  an engineer wrote into `data-track` attributes.
- **Who the user is.** No user ID, email, organization or workspace is
  attached, and no identifier is stored in the browser.
- **The path inside a provider page**, which holds resource names.
- **The name of an organization's own provider.** It is chosen by that
  organization, so it is reported as `org-provider`.

The hub enforces the shape as well: an event with a query string in its route,
an unknown type or an oversized field is discarded there, whatever the portal
sent.

### The browser address

The hub passes the browser's IP address and user agent to Umami, which hashes
them with a rotating salt into an anonymous session ID and does not store the
address. This is what lets Umami tell two visitors apart, and it has a useful
side effect: if your marketing site reports to the **same website ID**, a
visitor's session on the site and their session in the console are the same
session, so the backend shows the path from landing page into the console
without any identifier being passed between the two.

That is still processing of personal data under GDPR, and under the ePrivacy
rules analytics may need consent depending on your jurisdiction and
configuration. Deciding that, and telling your users, is the operator's
responsibility.

## Adding events

Mark the control. No JavaScript is needed, in the portal or in a provider UI
(provider elements render in the portal document, so the same listener sees
them):

```html
<button data-track="enable_provider" data-track-placement="card">Enable</button>
```

- Names are `lower_snake_case`, start with a letter, at most 64 characters.
- Labels are short fixed strings. Never bind tenant data (a resource name, an
  email, anything typed) into a `data-track-*` attribute.
- A click on a submit button records the attempt, not the outcome.

Host code can call `trackEvent(name, props)` from `@/telemetry/install` for
interactions that are not clicks.

## Changing the backend

The portal and the providers speak only the neutral event shape above. The one
place that knows about Umami is `pkg/hub/telemetry/umami.go`, behind the
`Sink` interface in the same package.
