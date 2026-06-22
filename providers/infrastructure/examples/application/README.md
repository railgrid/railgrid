# `application` template — 3-tier demo apps

Example workloads for the infrastructure provider's **`application`** template (a
3-tier app: frontend + backend + Postgres). They exist so the template can be
provisioned from the portal in essentially one click and visibly "just work".

```
browser ──▶ frontend (Go, this dir/frontend) ──▶ backend (Go, this dir/backend) ──▶ Postgres
            renders a guestbook page            JSON API over DATABASE_URL        (provisioned by the template)
```

- **[apps/frontend](apps/frontend)** — public-facing tier (the only one exposed).
  Standard-library Go HTTP server: renders a guestbook page, calls the backend
  over cluster DNS (`BACKEND_URL`). Never touches Postgres directly.
- **[apps/backend](apps/backend)** — internal API tier. Go + a pure-Go Postgres
  driver; reads/writes the guestbook in Postgres via `DATABASE_URL` (the
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
excludes those resources via `includeWhen`) and routes the Ingress straight to
the frontend.

> ⚠️ `oidc.mode: none` means the app is **unauthenticated** — anyone with the URL
> can reach it. It is for demos/dev only. For real use, set `oidc.mode: byo`,
> supply `oidc.issuerURL` + `oidc.clientID`, and put the client secret in your
> `cloud-credentials` Secret under `oidc_client_secret` (see
> [docs/application-template-architecture.md](../../docs/application-template-architecture.md)).

## Run locally

```sh
# backend needs a Postgres; point DATABASE_URL at one
DATABASE_URL=postgres://user:pass@localhost:5432/appdb?sslmode=disable \
  go run ./apps/backend            # listens on :8080

BACKEND_URL=http://localhost:8080 PORT=8081 \
  go run ./apps/frontend           # open http://localhost:8081
```
