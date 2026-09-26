# Quickstart provider portal

Vite + TypeScript micro-frontend for the railgrid quickstart provider. Built
into `dist/` and embedded into the provider binary via `//go:embed` (see
`../assets.go`). The railgrid hub proxies requests under
`/ui/providers/quickstart/` to the provider's HTTP server, which serves
the embedded build output.

## Layout

- `src/main.ts` — entry script the portal loads as a one-shot `<script>`.
  Registers `<railgrid-provider-quickstart>` and the optional
  `<railgrid-dashboard-tile-quickstart>`.
- `src/element.ts` — the provider page. Lists and creates Greetings with
  `portalkit/kube.ts createKubeClient` over `/clusters/{id}`, and calls the
  provider's one data-plane verb the same way — it is a kcp custom
  subresource, addressed with the client's `verbPath(...)` and sent through
  `providerFetch`; nothing goes to the hub's `/services/providers/` backend
  proxy. Renders in light DOM so the portal's CSS custom properties cascade in.
- `src/tile.ts` — the dashboard card, built on `portalkit/dashboardtile.ts`.
- `src/portalkit/` — vendored copy of `provider-sdk/portalkit`. Never edit it:
  edit the canonical copy and run `make sync-portalkit`.
- `src/style.css` — element styles, namespaced under the tag name and
  attached once as a `<style>` in `<head>`.
- `public/icon.svg` — provider tile icon shown in the portal side-nav.
- `public/index.html` — fallback page served when a browser visits the
  provider URL directly (debug aid).

## Develop

```sh
npm install
npm run dev       # vite dev server with HMR
npm run build     # produce dist/
npm run typecheck # tsc --noEmit
npm test          # layout + data-path assertions (node --test)
```

The Go binary embeds `dist/`, so a full rebuild is:

```sh
make build-quickstart-provider   # runs npm install + npm run build + go build
```

## Scaling up

This template emits a single `main.js` (library/IIFE) for the entry, but
Rollup will code-split dynamic `import()` calls into hashed chunks under
`dist/assets/`. The hub's UI proxy treats anything with a `.` in the last
segment as a static asset and forwards it to this binary, so async chunks,
images, etc. all round-trip without further hub-side configuration.

To use a framework (Vue, React, Lit, …), import it in `src/main.ts` and
mount it inside the element's light DOM during `connectedCallback`. The
public contract with the portal (one entry script, one custom element,
the `railgridContext` property setter, the `railgrid-navigate` CustomEvent)
stays unchanged.
