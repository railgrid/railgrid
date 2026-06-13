# Kuery provider portal

Self-contained Vite + TypeScript project that builds the custom element the
kedge portal loads under `/ui/providers/kuery/`. The build output
(`dist/`) is embedded into the provider binary via `//go:embed` (see
`../assets.go`), so `npm run build` must run before `go build` — the
`make build-kuery-provider` target from the kedge repo root chains the two.

The UI is a fleet inventory table plus an impact drill-down. The impact view
renders the selected object's declared blast radius as an interactive
node-link graph (anchor in the center, related objects as nodes, edges
colored by relation type); tapping a node re-anchors on it. A List/Graph
toggle keeps the original grouped-list view as a fallback. See
`docs/kuery-provider-architecture.md` in the kedge repo.

The graph uses Cytoscape, but the portal build is IIFE library mode and can't
emit lazy bundler chunks, so Cytoscape is vendored as a static asset rather
than imported: `npm run build` copies `cytoscape.min.js` into `dist/` (the
`vendor-cytoscape` script) and `graph.ts` injects it via a `<script>` tag the
first time the graph opens. So `main.js` stays small (~6 kB gzip) and the
~145 kB Cytoscape payload only loads when someone actually views a graph.

```bash
npm install
npm run build      # → dist/ (main.js + cytoscape.min.js + icon.svg + index.html)
npm run dev        # standalone debug page on the Vite dev server
```
