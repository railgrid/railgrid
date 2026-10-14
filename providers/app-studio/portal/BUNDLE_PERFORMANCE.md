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
production graph guard rejects all `@vue/compiler-*` modules even in shared or
lazy chunks. A source regression test checks the portal and canonical PortalKit
and AgentKit sources for literal runtime `template:` options in JS/TS modules;
use SFC templates or render functions instead.

Lazy-load optional settings and dialogs when that reduces the initial page path,
but keep shared dependencies deduplicated. Moving code into lazy chunks does not
reduce the total asset budget. Preserve the classic-script bootstrap and hashed
dynamic imports required by the provider host.

Switching to runtime-only Vue reduced measured gzip bytes from 91,226 to 58,423
for the dashboard, from 353,309 to 320,504 for the page, and from 384,857 to
352,141 for total assets on the original comparison baseline. These are bundle
measurements, not browser timing measurements. The largest remaining module is
`App.vue`; future feature work should extract optional surfaces from its eager
dependency graph instead of continuing to grow that entry.

After merging the shared composer changes from main, the optimized build measures
58,421 B gzip for the dashboard, 320,592 B for the page, and 352,232 B total.
Budgets are ratcheted down to preserve the savings while leaving a deliberate
3–5 KB gzip margin (rather than the former tens of bytes):

| Path | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: |
| Dashboard | 200,000 B | 62,000 B | 3,579 B |
| Page | 1,180,000 B | 325,000 B | 4,408 B |
| Total assets | 1,275,000 B | 357,000 B | 4,768 B |

Bootstrap and authentication settings limits remain unchanged. Larger growth
requires investigating its module costs, not automatically moving the ceilings.
