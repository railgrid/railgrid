# Kuery provider portal

Self-contained Vite + TypeScript project that builds the custom element the
kedge portal loads under `/ui/providers/kuery/`. The build output
(`dist/`) is embedded into the provider binary via `//go:embed` (see
`../assets.go`), so `npm run build` must run before `go build` — the
`make build-kuery-provider` target from the kedge repo root chains the two.

Phase 1 renders a placeholder: the portal-handshake panel and a backend
round-trip against `/api/status`. The Phase 3 UI (inventory table, object
graph, impact view) replaces it — see
`docs/kuery-provider-architecture.md` in the kedge repo.

```bash
npm install
npm run build      # → dist/ (main.js + icon.svg + index.html)
npm run dev        # standalone debug page on the Vite dev server
```
