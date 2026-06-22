# `application` template — 3-tier demo apps

Example workloads for the infrastructure provider's **`application`** template (a
3-tier app: frontend + backend + Postgres). They exist so the template can be
provisioned from the portal in essentially one click and visibly "just work".

Both tiers are served under **one URL**: `/` is the UI, `/api/*` is the backend.
The exposure layer does the path routing — the Ingress in `oidc.mode=none`, and
oauth2-proxy (multi-upstream) otherwise, so the backend stays behind the gate.

```
                 one URL (https://<app>.<baseDomain>)
                          │
        ┌── / ───────────┴──────────── /api ──┐
        ▼                                      ▼
   frontend (Go, this dir/frontend)      backend (Go, this dir/backend) ──▶ Postgres
   UI; calls /api from the browser       JSON API over DATABASE_URL
```

- **[apps/frontend](apps/frontend)** — public UI tier. Standard-library Go server
  that serves a small SPA; the page calls `/api/messages` from the **browser**
  (same origin), so the frontend never proxies to the backend and needs no
  `BACKEND_URL`.
- **[apps/backend](apps/backend)** — API tier, served at `/api/*`. Go + a pure-Go
  Postgres driver; reads/writes the guestbook in Postgres via `DATABASE_URL` (the
  connection string the template's credentials Job mints).

Each app is a self-contained Go module with its own `Dockerfile` (build context =
the app dir).

## Images

[`.github/workflows/infrastructure-examples.yaml`](../../../../.github/workflows/infrastructure-examples.yaml)
builds and pushes both on every change to `:latest` (multi-arch on `main`,
build-only on PRs):

- `ghcr.io/faroshq/kedge-infrastructure-example-frontend:latest`
- `ghcr.io/faroshq/kedge-infrastructure-example-backend:latest`

The template's `spec.sampleValues`
([install/templates/application.yaml](../../install/templates/application.yaml))
pre-fills the provision form with these image refs, so the portal demo always
pulls a current build.

## One-click demo & the `oidc.mode: none` caveat

The `application` template is OIDC-gated by design. For a zero-setup demo the
sample uses **`oidc.mode: none`**, which drops the oauth2-proxy gate (the RGD
excludes those resources via `includeWhen`) and routes via the Ingress directly
(`/` → frontend, `/api` → backend). In `byo` mode oauth2-proxy owns the URL and
path-routes the same `/` and `/api` behind authentication.

> ⚠️ `oidc.mode: none` means the app is **unauthenticated** — anyone with the URL
> can reach it. It is for demos/dev only. For real use, set `oidc.mode: byo`,
> supply `oidc.issuerURL` + `oidc.clientID`, and put the client secret in your
> `cloud-credentials` Secret under `oidc_client_secret` (see
> [docs/application-template-architecture.md](../../docs/application-template-architecture.md)).

## Run locally

There's no path router locally, so hit each tier directly:

```sh
# backend needs a Postgres; point DATABASE_URL at one
DATABASE_URL=postgres://user:pass@localhost:5432/appdb?sslmode=disable \
  go run ./apps/backend            # API on :8080 (GET/POST /api/messages)

PORT=8081 go run ./apps/frontend   # UI on :8081 — but its /api calls are
                                   # same-origin (:8081), where there's no
                                   # backend. In-cluster the exposure layer
                                   # routes /api to the backend; to mimic that
                                   # locally, front both behind one proxy that
                                   # sends /api → :8080 and / → :8081.
```
