# App Studio bundle performance

Build and enforce the production budgets from the repository root:

```sh
make build-app-studio-provider-portal
```

To inspect the largest contributing modules:

```sh
APP_STUDIO_BUNDLE_REPORT=1 make build-app-studio-provider-portal
```

The module report shows rendered bytes before minification. The budget report
measures emitted raw and gzip bytes, including static dependency closures for
the dashboard and page and every emitted file for the total. It prints remaining
headroom; failures also list the largest compressed assets.

Treat budgets as ceilings, not snapshots to increase with every feature. First
check for unused dependencies, full library builds, and duplicated code. Vue
must use the runtime-only build: Vite compiles SFC templates at build time. The
production graph guard rejects compiler modules even in shared or lazy chunks.

Lazy-load optional settings and dialogs when that reduces the initial page path,
but keep shared dependencies deduplicated. Moving code into lazy chunks does not
reduce the total asset budget. Preserve the classic-script bootstrap and hashed
dynamic imports required by the provider host.

Switching to runtime-only Vue reduced measured gzip bytes from 91,226 to 58,423
for the dashboard, from 353,309 to 320,504 for the page, and from 384,857 to
352,141 for total assets, with every existing budget unchanged. These are bundle
measurements, not browser timing measurements. The largest remaining module is
`App.vue`; future feature work should extract optional surfaces from its eager
dependency graph instead of continuing to grow that entry.
