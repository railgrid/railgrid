# Move the App Studio browser worker to the infrastructure provider

## Why

Today app-studio ships its **own** headless-browser service:

- `providers/app-studio/browser-worker/` — a custom Playwright service exposing
  `POST /v1/inspect`, image `@faroshq/app-studio-browser-worker`, port 8090.
- Deployed as a **standalone tilt resource** (`app-studio-browser-worker`), a
  hard `resource_deps` of the `app-studio` process.
- Reached by the backend through the `APP_STUDIO_BROWSER_WORKER_URL` env var
  (`main.go` → `ConfigurePreviewInspection`), one fixed URL for the whole
  provider.

This is the wrong altitude. app-studio already provisions its *other* shared
service — web search — the right way: the **Studio reconciler** creates one
`searxng` instance per workspace through the **infrastructure provider**, and
the backend reaches it over the infra **data-plane proxy verb**
(`.../dataplane/.../searxngs/{name}/proxy/search`). No env URL, no bespoke
deployment, no app-studio-owned image.

The infrastructure provider **already has the browser equivalent**:
`providers/infrastructure/install/templates/browser.yaml` — *"Browser
(Playwright MCP)"*, `instanceCRD: Browser`, same shape as searxng (size
buckets, `status.ready`, data-plane `proxy` verb, one replica, no token). It
runs the official `mcr.microsoft.com/playwright/mcp` server and exposes the
standard Playwright MCP toolset (`browser_navigate`, `browser_snapshot`,
`browser_take_screenshot`, `browser_console_messages`, …).

**Goal:** retire the app-studio custom worker; provision the `browser` template
once per workspace via the Studio reconciler; drive preview inspection through
the shared instance's data-plane MCP endpoint — exactly the search pattern.

## Target architecture (mirrors search)

| Concern            | Search (today)                                    | Browser (target)                                   |
|--------------------|---------------------------------------------------|----------------------------------------------------|
| Infra template     | `searxng`                                          | `browser` (already exists)                         |
| Instance CRD       | `Searxng`                                          | `Browser` (already exists)                         |
| Shared instance    | `app-studio-search` (fixed name)                  | `app-studio-browser` (fixed name)                  |
| Owner/lifecycle    | Studio reconciler `converge` + finalizer          | Studio reconciler, same `converge` + finalizer     |
| Studio CR field    | `spec.search` / `status.search`                   | `spec.browser` / `status.browser`                  |
| Ref resolution     | `searchResourceRef` → `fetchProjectTemplate("searxng")` | `browserResourceRef` → `fetchProjectTemplate("browser")` |
| Permission claim   | `searxngs.infrastructure…`                        | `browsers.infrastructure…` (new claim)             |
| Backend reaches it | infra data-plane `…/searxngs/{name}/proxy/search` | infra data-plane `…/browsers/{name}/proxy` (MCP root) |

## Decision — inspection contract

The current `inspect_development_preview` / `get_preview_console_logs` tools call
the custom `/v1/inspect` and get back a rendered-state result + console logs.
The `browser` template speaks **Playwright MCP** instead. Recommended: **adopt
Playwright MCP** and delete the custom contract — every capability maps 1:1:

- navigate → `browser_navigate`
- rendered state / role+text assertions → `browser_snapshot` (accessibility tree)
- screenshot → `browser_take_screenshot`
- console / pageerror capture → `browser_console_messages`

Rejected alternative: keep `/v1/inspect` and just templatize the app-studio
image. That preserves the backend but keeps an app-studio-owned image + a
non-standard contract, i.e. it doesn't actually adopt "the same tooling." Only
choose it if `browser_snapshot` proves insufficient for the assertions the
inspector makes (see Phase 1 spike).

## Plan

### Phase 0 — confirm the template is production-ready (0.5d)
- [ ] Verify `browser` template + `Browser` CRD are seeded in the infra provider
  (`seedtemplates_test.go` references it; confirm the RGD reconciles and an
  instance reaches `status.ready`).
- [ ] Confirm app-studio's export can claim `browsers.infrastructure.kedge.faros.sh`
  — the infra identityHash is the same one already used for
  `applications`/`searxngs` (`372fcfe2…` in dev).

### Phase 1 — inspection client spike (1d)
- [ ] Stand up one `browser` instance by hand; from the backend, drive it over
  the data-plane proxy (`…/browsers/{name}/proxy`) using the tenant credential
  the assistant already holds (same hop `hubmcp` uses).
- [ ] Prove `browser_navigate` + `browser_snapshot` + `browser_console_messages`
  reproduce the current `inspect_development_preview` result fields. This
  validates the Decision above before touching the reconciler.

### Phase 2 — Studio owns a shared Browser instance (1d)
- [ ] `apis/ai/v1alpha1/types_studio.go`: add `StudioBrowser` (mirror
  `StudioSearch`: `Disabled`, `Size`, `ResourceRef`) to `StudioSpec.Browser`
  and `StudioServiceStatus` to `StudioStatus.Browser`.
- [ ] `controller/studio/controller.go`: generalize `converge`/`ensureInstance`/
  `deleteInstance`/`finalize` to iterate over both services (search + browser),
  or add a parallel browser path. Add `BrowserInstanceName = "app-studio-browser"`
  and `browserTemplate = "browser"`.
- [ ] `api/studio_sessions.go`: add `browserResourceRef` →
  `fetchProjectTemplate("browser")`, set it on Studio create alongside search.
- [ ] Regenerate CRDs/schemas (`studios.ai.kedge.faros.sh`) — note this bumps the
  studios APIResourceSchema; re-bootstrap the export (see Ops caveat).

### Phase 3 — permission claim + bootstrap (0.5d)
- [ ] Add `browsers.infrastructure.kedge.faros.sh` to the export's permission
  claims in the **three** sync points: `init_cmd.go`
  (`instanceClaimResources`), `manifest.yaml`, `catalogentry.yaml` — with the
  infra identityHash (same as the other infra claims).
- [ ] Re-bootstrap; confirm tenant bindings apply the new claim (`Accepted`, not
  `Rejected`).

### Phase 4 — cut the backend over (1d)
- [ ] `api/assistant_preview_inspection.go`: replace the `/v1/inspect` HTTP
  client with the data-plane MCP client from Phase 1; resolve the shared
  instance from `Studio.status.browser` (fixed name `app-studio-browser`),
  exactly as search is addressed by fixed name.
- [ ] Delete `ConfigurePreviewInspection(APP_STUDIO_BROWSER_WORKER_URL)` from
  `main.go`; drop the env var.
- [ ] Update the `inspect_development_preview` / `get_preview_console_logs` tool
  handlers to the new result mapping.

### Phase 5 — delete the old worker (0.5d)
- [ ] Remove `providers/app-studio/browser-worker/` (src, Dockerfile,
  package.json).
- [ ] Remove the tilt resources: `app-studio-browser-worker`,
  `docker-build-app-studio-browser-worker`, `run-app-studio-browser-worker`,
  and the `resource_deps`/`dev-agent-image` wiring in `Tiltfile.cluster`.
- [ ] Remove Makefile targets (`build/run/test/docker-build-app-studio-browser-worker`,
  `APP_STUDIO_BROWSER_WORKER_*` vars) and the Helm/chart wiring.
- [ ] Grep for `browser-worker`, `BROWSER_WORKER`, `8090`, `/v1/inspect` to
  ensure nothing dangles.

## Ops caveat (learned the hard way this session)

Re-bootstrapping the app-studio export is **not** free:
- `ApplyAPIExport` **deletes and recreates** the export when it can't update in
  place. That changes the export's `identityHash` **and reassigns its shard**,
  which forces every tenant APIBinding to re-bind and can cascade-delete
  in-flight Projects. Do Phase 2–3 bootstraps in a quiet window.
- Every non-built-in permission claim needs the **current** owning-export
  `identityHash`. The `browsers` claim uses the infra hash — discover it live
  (`get apiexport infrastructure.providers.kedge.faros.sh -o jsonpath=…`), never
  a memorized value; infra's hash has already rotated once (`4d31761a…` →
  `372fcfe2…`).
- After the export changes, tenant bindings may re-add the claim as `Rejected`
  — patch `state: Accepted` (or fix the hub enablement to accept on re-sync).

## Rough size

~4 engineering days. No new image to build/publish (the template uses upstream
`playwright/mcp`), which is most of the win: app-studio stops owning a browser
image and a deployment, and preview inspection rides the same data-plane path as
every other shared instance.
