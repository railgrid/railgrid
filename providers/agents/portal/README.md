# Agents portal

Vite + TypeScript micro-frontend for the agents provider, mounted in the kedge
portal under `/ui/providers/agents/`. The Go binary embeds `portal/dist` via
`assets.go`.

Build:

```
npm install
npm run build   # → portal/dist, embedded at go build time
```

The portal handshakes with the host portal via `postMessage`: it posts
`kedge.ready` and receives `kedge.context` with `{user, tenant, theme, basePath}`.

Milestone 2 replaces the static `dist/index.html` shell with the real app
(agent list, chat with streaming, schedules, connections).
