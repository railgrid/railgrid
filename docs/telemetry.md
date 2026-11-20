---
layout: default
title: Telemetry
description: "Anonymous console usage and instance reporting, on by default: what is sent, what never is, and how to turn it off"
---

# Telemetry

A released Railgrid hub reports two things to Railgrid, anonymously, unless
you turn it off:

- **Console usage**: which pages of the console are opened and which marked
  controls are clicked. It shows where people go and where they stop.
- **An instance report**: once a day, that this installation exists, which
  version it runs and roughly how big it is. It is how Railgrid knows how many
  self-hosted hubs there are and which versions are still in use.

Both go to `https://telemetry.railgrid.ai`, a self-hosted
[Umami](https://umami.is) (open source) run by Railgrid. Neither carries a
hostname, a user, an organization, a workspace or a resource name, and no
browser address is sent. [What is never sent](#what-is-never-sent) is the full
list; [the instance report](#the-instance-report) shows the whole payload.

Only a tagged release (`v1.2.3`, `v1.2.3-rc.1`) reports to Railgrid. A hub
built from a checkout, run by `make tilt` or started by a test suite has no
built-in destination and sends nothing.

## Turning it off

```yaml
# railgrid-hub chart values
hub:
  telemetry:
    enabled: false
```

or `--telemetry=false` on the binary. A hub started this way:

- registers no telemetry route (`POST /api/telemetry/events` and
  `POST /api/telemetry/public` are refused like any unknown API path),
- answers `"telemetry": false` on `/healthz`, which is the portal's only
  signal, so the portal records nothing, stores nothing and sends nothing,
- sends no instance report and makes no outbound telemetry call at all.

To keep console usage and turn off only the instance report:

```yaml
hub:
  telemetry:
    instanceReport: false
```

or `--telemetry-instance-report=false`.

A hub whose network cannot reach `telemetry.railgrid.ai` needs no setting:
delivery is best effort, nothing is retried or queued without bound, and a
request is never slowed by it.

To check what a running hub does, look at `/healthz` (`"telemetry": true` or
`false` is whether the console reports) and at the hub's startup log, which
says `Console telemetry enabled` and `Anonymous instance report enabled` with
the destination when they are on, and nothing when they are off.

## Sending console usage to your own backend

To see how your own users use the console, point the hub at an Umami instance
you run. Create a website in it and give the hub its URL and website ID:

```yaml
hub:
  telemetry:
    url: https://telemetry.example.com
    websiteID: 6ac408ea-8b0d-4a6e-b7f1-5d369db5bb19
```

or `--telemetry-url` and `--telemetry-website-id` on the binary. Both are
required together; the hub refuses to start with one of them.

Console events then go **only** to your backend and none go to Railgrid. The
instance report is separate and still goes to Railgrid until you turn it off
as above. This works on any build, a development one included.

The portal picks a change up on its next load. No rebuild is needed: the
tracker ships in every portal bundle and stays inert until `/healthz` says
telemetry is on.

Your own backend gets more than Railgrid's does, because it is yours: the
hub's external hostname, the browser's address (see
[The browser address](#the-browser-address)), the origin a user arrived from,
and the names of providers you registered yourself. Set
`hub.trustedProxyCIDRs` (`--trusted-proxy-cidrs`) if the hub sits behind a
proxy. Without it the hub sees every browser as the proxy's address, and the
backend then merges users who share a browser version into one session.

## The instance report

One event a day, the first one five to ten minutes after the hub starts, sent
by the hub itself (the leader, when it is scaled). This is all of it:

| Field | Example | Notes |
| ----- | ------- | ----- |
| Instance ID | `3f1c9a52-7d0e-4b6a-9c11-0e5d2a7b8f34` | a one-way hash of an internal kcp UID; see below |
| `version` | `v0.3.0` | the hub's release |
| `kcp` | `embedded` \| `external` | |
| `auth` | `oidc` \| `static` \| `oidc+static` | which sign-in is configured, not its issuer |
| `organizations` | `6-20` | a size class, never the count; personal organizations excluded |
| `users` | `101-500` | a size class, never the count |
| `providers` | `agents,edges` | platform providers installed, **only** those Railgrid publishes |
| `other_providers` | `1` | a size class of how many others there are; their names are not sent |

The size classes are `0`, `1`, `2-5`, `6-20`, `21-100`, `101-500` and `500+`.

The **instance ID** is derived inside the hub by hashing the UID kcp assigned
to one of the hub's own workspaces when it was installed. It is random, it
cannot be turned back into anything about the installation, and it is the same
tomorrow, which is all it is for: counting an installation once. Nothing is
stored to produce it, and it is in the hub's startup log if you want to see
yours.

Like any request, the report arrives from an address: the hub's outbound one.
Umami derives a country and region from it and does not store the address.

## How console usage flows

```
browser ── POST /api/telemetry/events ──▶ hub ── POST {url}/api/send ──▶ Umami
          (same origin, signed-in only)    ▲  (the hub's own outbound call)
                                           │
              the hub's own API handlers ──┘  (confirmed outcomes)
```

The portal's Content-Security-Policy stays `connect-src 'self'`: the browser
talks only to the hub, never to Railgrid or to any analytics backend. The hub
validates each event, answers `204` at once and delivers from a bounded queue
in the background. Delivery is best effort: when the backend is slow or down,
events are dropped; a request is never slowed and nothing is retried.

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
| Screen size, language | `1440x900`, `en-GB` | |
| User agent | | the browser's |
| Instance ID | | to Railgrid only: the same ID as in the instance report, recorded as the host |
| Arrival origin | `https://railgrid.ai` | **your own backend only**; first page only; scheme and host, no path |
| Browser address | | **your own backend only**, see below |
| Hub hostname | `console.example.com` | **your own backend only** |

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
- **To Railgrid: the browser's address.** The hub does not forward it. A
  session there is a hash of the instance ID and the user agent, so everyone
  on one browser version in one installation is a single session.
- **To Railgrid: where the installation is.** No hostname and no arrival
  origin, which on a self-hosted hub is as likely an intranet address as a
  public site. The host recorded is the instance ID.
- **To Railgrid: a provider name you chose.** Only the providers Railgrid
  publishes are reported by name. Any other platform provider is `other`, in
  the route and in the `provider` label alike.
- **The path inside a provider page**, which holds resource names.
- **The name of an organization's own provider.** It is chosen by that
  organization, so it is reported as `org-provider`.

The hub enforces the shape as well: an event with a query string in its route,
an unknown type or an oversized field is discarded there, whatever the portal
sent.

### The browser address

This applies only when you send console usage to **your own** backend; the
hub never forwards a browser address to Railgrid.

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
`Sink` interface in the same package. The built-in destination and everything
withheld from it are in `builtin.go`; the instance report is `instance.go`,
and `Reporter.build` is the only place its payload is assembled.
