# Quickstart provider portal

Vite + TypeScript micro-frontend for the kedge quickstart provider. Built
into `dist/` and embedded into the provider binary via `//go:embed` (see
`../assets.go`). The kedge hub proxies requests under
`/ui/providers/quickstart/` to the provider's HTTP server, which serves
the embedded build output.

## Layout

- `src/main.ts` — entry script the portal loads as a one-shot `<script>`.
  Registers the `<kedge-provider-quickstart>` custom element.
- `src/element.ts` — the element class itself. Renders in light DOM so
  the portal's CSS custom properties cascade in.
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
the `kedgeContext` property setter, the `kedge-navigate` CustomEvent)
stays unchanged.
