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

- registers no telemetry route (`POST /api/telemetry/events` and
  `POST /api/telemetry/public` answer 404),
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
          (same origin, signed-in only)    ▲  (the hub's own outbound call)
                                           │
              the hub's own API handlers ──┘  (confirmed outcomes)
```

The portal's Content-Security-Policy stays `connect-src 'self'`: the browser
talks only to the hub. The hub validates each event, answers `204` at once and
delivers from a bounded queue in the background. Delivery is best effort: when
the backend is slow or down, events are dropped; a request is never slowed and
nothing is retried.

Only signed-in callers can post to `/api/telemetry/events`, so it cannot be
used as an open proxy to the backend.

### Before sign-in

The sign-in page is the one step the authenticated route cannot see, and it is
where a visitor who clicked "Get started" on the marketing site either signs
in or leaves. So one more route, `POST /api/telemetry/public`, takes no
credentials and accepts exactly three things: a page view of `login`, a page
view of `auth-callback`, and the `sign_in` click with `method: oidc | token`.
Anything else in the request is discarded, the batch is capped at four events
and 2 KiB, and each address is limited to a burst of 30 requests refilled at
one every two seconds. A caller over the limit gets the same `204` and
nothing is recorded.

### Outcomes the hub confirms

A browser can report that a button was clicked, not that the operation
worked. For the operations the hub performs itself, the hub records the
outcome when it succeeds, under the caller's own browser address so it joins
the same session as the clicks before it. These also fire for the CLI and
the API, where there is no portal at all.

| Event | Labels |
| ----- | ------ |
| `organization_created` | |
| `workspace_created` | |
| `provider_enabled` | `provider`; `flow`: `enable` \| `setup` |

They are recorded with the route `/hub`, never the request path.

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
(`/:orgID/:workspaceID/providers/agents`), once per visit: moving around
inside a provider is not a new page view.

### Events the console records

Clicks tell you what was tried; the outcome events tell you whether it
worked. Together they make each flow a funnel.

| Flow | Events | Labels |
| ---- | ------ | ------ |
| First sign-in (`/onboarding`) | `onboarding_submit`, `onboarding_completed`, `onboarding_failed`, `onboarding_start_over` | `start`: `personal` \| `organization`; `plan`; `stage`: `organization` \| `checkout` \| `workspace` \| `activation` |
| Checkout | `onboarding_checkout_opened`, `onboarding_checkout_returned`, `onboarding_checkout_canceled`, `contact_sales` | `plan` |
| Getting-started guide | `guide_step`, `guide_setup_started`, `guide_setup_completed`, `guide_setup_failed`, `guide_open_goal`, `guide_dismissed`, `guide_explore_providers`, `guide_browse_providers`, `guide_request_copied`, `open_getting_started` | `step`: `audience` \| `goal` \| `setup`; `role`: `admin` \| `member`; `audience`; `goal`; `state`: `ready` \| `needs_setup` \| `blocked` \| `needs_admin` \| `unknown` \| `explore`; `stage`: `stale` \| `access_changed` \| `unavailable` \| `enable` \| `not_ready` |
| Sign-in | `sign_in` (click), page views of `login` and `auth-callback` | `method`: `oidc` \| `token` |
| Workspaces | `create_workspace` (click), `workspace_create_failed`, `review_plans`; `workspace_created` from the hub | `placement`: `first` \| `dialog`; `reason`: `limit` \| `error` |
| Organizations | `organization_create_failed`; `organization_created` from the hub | |
| Providers | `enable_provider` (click), `provider_enable_failed`; `provider_enabled` from the hub | `provider`; `flow`: `enable` \| `setup`; `status`: HTTP status or `network` |
| Shell | `open_help`, `open_cli_quickstart` | |

Success comes from the hub and failure from the browser on purpose: the hub
knows the operation completed, and the browser knows what the user was shown
when it did not.

`guide_step` with `step: setup` carries what the user found on the last step.
`state: needs_admin` is a member whose administrator has not set the Workspace
up; `state: blocked` is an administrator whose setup cannot proceed.

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

Host code calls `trackEvent(name, props)` from `@/telemetry/track` for what
is not a click: an outcome (`workspace_created`, `workspace_create_failed`) or
a step in a flow. A failure is reported by a **fixed label** naming where it
stopped (`stage: checkout`, `reason: limit`, an HTTP status). Never pass the
error message: it routinely quotes an organization, workspace or provider
name. Add the event to the table above.

### In a provider UI

A click works exactly as above. For an outcome, call
`providerTrack(ctx)(name, props)` from `portalkit/telemetry`, where `ctx` is
the `railgridContext` the provider already holds:

```ts
import { providerTrack } from './portalkit/telemetry'

providerTrack(ctx)('edge_created', { kind: 'kubernetes' })
```

The host labels every provider event with `provider: <name>`; a bundle cannot
set or override it. **Only platform providers report.** An organization's own
provider is code that organization wrote, so the event names and labels in it
are whatever it chose: the host drops every click inside such a provider's
element and hands it a `track` function that records nothing.

What the platform providers report today (each provider's `portal/src/telemetry.ts`
and the `data-track` attributes in its views are the full list):

| Provider | Flows covered | Labels |
| -------- | ------------- | ------ |
| `edges` | Connect an edge (`edge_connect_started`, `edge_created`, `edge_create_failed`, `edge_command_copied`, `edge_connected`, `edge_connect_skipped`, `edge_connect_cancelled`), services, workloads, deletes, terminal | `kind`: `kubernetes` \| `server` \| `macos`; `placement`; `reason` |
| `agents` | Connect a model (`model_connect_*`, `model_connected`), create an agent (`agent_create_*`, `agent_created`), chat and runs (`chat_message_sent`, `run_started`, `run_finished`, `run_failed`), approvals, schedules, triggers, connections, webhooks, skills | `backend`: `model` \| `harness`; `status`; `decision`; `type` (connection type); `reason` |
| `app-studio` | Connect a model, create a project (`project_create_step`, `project_created`, `project_create_failed`), prompts (`session_prompt_sent`, `session_run_finished`), preview (`preview_ready`, `preview_failed`), deploy (`publish_deploy_*`), sharing | `source`: `prompt` \| `import`; `step`; `stage`; `kind`; `reason` |

`reason` is always derived from the HTTP status (`forbidden`, `conflict`,
`validation`, `limit`, `error`, ...), never from the message. Message text,
prompts, resource names, model IDs and template names are not reported.

## Changing the backend

The portal and the providers speak only the neutral event shape above. The one
place that knows about Umami is `pkg/hub/telemetry/umami.go`, behind the
`Sink` interface in the same package.
