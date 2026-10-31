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
At that point, budgets were ratcheted down to preserve the savings while leaving
a deliberate 3–5 KB gzip margin (rather than the former tens of bytes). The
combined reasoning picker uses that headroom as recorded below.

The model-aware reasoning controls previously measured 1,197,690 B raw /
328,296 B gzip on the page path and 1,295,478 B raw / 360,062 B gzip across all
emitted assets. The combined model-and-reasoning popover measures 1,214,190 B
raw / 330,623 B gzip on the page path and 1,321,969 B raw / 364,905 B gzip
across all assets. This is a measured increase of 16,500 B raw / 2,327 B gzip
on the page path and 26,491 B raw / 4,843 B gzip across all assets. The initial
`AIModelReasoningPicker.vue` report showed 32,680 rendered bytes before
minification; the final measurements above include the three review passes. The picker
replaces the adjacent composer model and reasoning controls;
the report showed no duplicated large dependency closure. The adjusted page
raw ceiling leaves 5,810 B of headroom, its gzip ceiling leaves 1,377 B, and
the adjusted total ceilings leave 8,031 B raw / 3,095 B gzip.

| Path | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: |
| Dashboard | 200,000 B | 62,000 B | 3,442 B |
| Page | 1,220,000 B | 332,000 B | 1,377 B |
| Total assets | 1,330,000 B | 368,000 B | 3,095 B |

Bootstrap and authentication settings limits remain unchanged. Larger growth
requires investigating its module costs, not automatically moving the ceilings.

With PortalKit column sorting, repository connection settings load on demand
and the production minifier runs three compression passes. The measured page
path is 1,215,445 B raw / 331,358 B gzip; total assets are 1,328,399 B raw /
367,745 B gzip. These retain the existing ceilings, including the total asset
gate that accounts for the additional lazy chunk. Loading and failure messages
remain available while repository settings load. These are local bundle
measurements, not live browser performance evidence.

With shared ResourceTimestamp styles, the local production build measures
1,216,686 B raw / 331,515 B gzip on the page path and 1,329,640 B raw /
367,900 B gzip across all assets. Compared with the post-sorting baseline,
this adds 1,241 B raw / 157 B gzip to the page path and 1,241 B raw / 155 B
gzip to all assets. The build passes the unchanged ceilings with 3,314 B raw /
485 B gzip page headroom and 360 B raw / 100 B gzip total headroom. These are
local bundle measurements, not live browser performance evidence.

The portal workflow diagnostics reached 1,329,980 B raw / 368,013 B gzip total,
exceeding the gzip ceiling by 13 B. A fourth minifier compression pass reduces
the page path to 1,217,009 B raw / 331,620 B gzip and total assets to
1,329,963 B raw / 367,991 B gzip without changing application code or copy.
The existing ceilings remain enforced, with only 37 B raw / 9 B gzip total
headroom. The focused `make test-ui-e2e-report-portal` target now includes the
production build and its budget gate; tests and typechecking alone do not
establish that the emitted bundle fits. These are local build measurements,
not live browser performance evidence.
