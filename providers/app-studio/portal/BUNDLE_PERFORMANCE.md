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
exceeding the old gzip ceiling by 13 B. A fourth minifier pass saved only
17 B raw / 22 B gzip and left 9 B gzip headroom; that workaround is superseded.
The minifier retains three passes.

A fresh `APP_STUDIO_BUNDLE_REPORT=1 make build-app-studio-provider-portal`
confirmed that `App.vue` remains the largest rendered module (676,171 B before
minification), followed by Vue runtime-core (136,511 B), canonical UI CSS
(86,711 B), and ResourceTable (70,219 B). The graph guard still excludes the
Vue compiler. The report did not identify an obvious unused large dependency;
extracting optional App.vue surfaces remains separate performance work. Splitting
chunks alone would not reduce the total asset budget.

The page and total ceilings deliberately restore about 4 KB of gzip margin for
small diagnostics and toolchain output variation. This accepts the accumulated
shared UI and diagnostics growth measured above, rather than depending on another
minifier pass to save a few bytes. The new ceilings are fixed, not percentages
that automatically grow with the bundle. Bootstrap, dashboard, and authentication
settings ceilings are unchanged.

| Path | Measured raw | Measured gzip | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: | ---: | ---: |
| Page | 1,217,026 B | 331,637 B | 1,230,000 B | 336,000 B | 4,363 B |
| Total assets | 1,329,980 B | 368,013 B | 1,345,000 B | 372,000 B | 3,987 B |

The focused `make test-ui-e2e-report-portal` target includes the production build
and its budget gate; tests and typechecking alone do not establish that the
emitted bundle fits. These are local build measurements, not live browser
performance evidence. Larger growth still requires module-cost investigation.

The provider-connection/model-binding revamp initially exceeded both startup
budgets and the total budget. Its model registry had pulled credential-write
transactions into the dashboard graph, and the Models surface retained a second
legacy credential form. The final implementation removes that form, uses the
same binding selector for legacy migration, and separates key-free metadata
reads from lazy registry mutations. `ModelsSettings.vue` remains lazy loaded.

The production module report excludes Vue's compiler and shows no duplicated
large dependency closure. The metadata reader is 2,681 B gzip and the mutation
chunk is 4,630 B gzip. Remaining model state in `App.vue` supports project
creation, model selection, reasoning, and chat; removing it would change those
flows. A separate read-only review found no further feature-specific reduction
large enough to close the remaining total gap. These are emitted bundle
measurements, not browser timing measurements.

The complete feature adds 50,101 B raw / 13,285 B gzip to all emitted assets
against the previous passing baseline. Its optimized dashboard and page fit
their existing ceilings. The total-only ceiling deliberately accepts that
measured optional feature cost with a fixed 50,000 B raw / 14,000 B gzip increase.
It leaves about the same margin as the previous baseline; bootstrap, dashboard,
page, and authentication settings ceilings remain unchanged.

| Path | Measured raw | Measured gzip | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: | ---: | ---: |
| Dashboard | 189,461 B | 57,826 B | 200,000 B | 62,000 B | 4,174 B |
| Page | 1,222,888 B | 332,544 B | 1,230,000 B | 336,000 B | 3,456 B |
| Total assets | 1,380,081 B | 381,298 B | 1,395,000 B | 386,000 B | 4,702 B |

Future changes still have to pass these fixed budgets and investigate growth;
the total limit does not track subsequent build output automatically.

The shared AgentKit composer extraction and App Studio reuse measure 1,399,893 B
raw / 387,753 B gzip across all emitted assets, compared with 1,380,081 B raw /
381,298 B gzip at the previous passing baseline. The final anchored command
palette and portable inline-preview controls add shared fixed-layer positioning
without changing the page limit; the page path measures 1,191,115 B raw /
323,464 B gzip. A read-only module-cost review found no duplicated large
dependency closure to remove without changing the feature. The total-assets
ceiling is 1,400,000 B raw / 388,000 B gzip, leaving 107 B raw / 247 B gzip of
fixed headroom. This is a measured ~0.5% increase over the previous gzip
ceiling; the page, dashboard, bootstrap, and authentication-settings limits
are unchanged.

| Path | Measured raw | Measured gzip | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: | ---: | ---: |
| Page | 1,191,115 B | 323,464 B | 1,230,000 B | 336,000 B | 12,536 B |
| Total assets | 1,399,893 B | 387,753 B | 1,400,000 B | 388,000 B | 247 B |

The final portable AgentKit receipt and skill-catalog accessibility/recovery
refinements measure 1,403,842 B raw / 388,421 B gzip across all emitted assets.
The page path measures 1,195,084 B raw / 324,148 B gzip; dashboard path measures
189,442 B raw / 57,799 B gzip. The shared catalog and receipt geometry remains
in the canonical AgentKit stylesheet so Agents and App Studio use the same
recipes; no provider-specific duplicate was introduced. The total-only ceiling
is now 1,405,000 B raw / 389,000 B gzip, leaving 1,158 B raw / 579 B gzip of
fixed headroom. Bootstrap, dashboard, page, and authentication-settings limits
are unchanged. This is a measured total-assets ceiling amendment, not automatic
growth with future bundle output.

| Path | Measured raw | Measured gzip | Raw limit | Gzip limit | Gzip headroom |
| --- | ---: | ---: | ---: | ---: | ---: |
| Dashboard | 189,442 B | 57,799 B | 200,000 B | 62,000 B | 4,201 B |
| Page | 1,195,084 B | 324,148 B | 1,230,000 B | 336,000 B | 11,852 B |
| Total assets | 1,403,842 B | 388,421 B | 1,405,000 B | 389,000 B | 579 B |

The subsequent input-geometry correction shares search icon/clear spacing across
Config, the library and the slash palette, anchors Add to its trigger, and uses
one composer focus frame. Emitted totals are 1,404,850 B raw / 388,569 B gzip;
the page path is 1,196,413 B raw / 324,379 B gzip. These fit the existing fixed
ceilings; no budget was increased for this correction.

The attachment recovery correction adds visible error reasons and scoped focus
recovery. Removing thumbnail utility declarations already owned by canonical
AgentKit CSS keeps totals at 1,404,779 B raw / 388,637 B gzip (page path:
1,195,547 B raw / 324,256 B gzip), within the unchanged ceilings. Touch thumbnails
are 104px so both 44px actions and their failure label have separate space.

The follow-up focus review shares one recovery guard between Retry and Remove
and checks focus immediately before restoring it. Final totals are 1,404,944 B
raw / 388,666 B gzip, within the same 1,405,000 B / 389,000 B ceilings.

The full-branch interaction review adds draft-bound chooser/batch handling and
IME guards. Removing an unused slash-token computation retains the existing
ceilings: final total 1,404,801 B raw / 388,635 B gzip.

The next accessibility review isolates palette/error IDs across Vue roots,
corrects disabled-option navigation, and clarifies chip removal. Consolidating
empty-state styling and making starter prompts respond to pane width retains
the existing ceilings: 1,404,776 B raw / 388,673 B gzip across all assets.

The palette lifecycle review adds focus-leave dismissal, guarded internal focus
transfers/selection commits, and rejected resource-lookup recovery. Shared
option-class reuse keeps final totals at 1,404,856 B raw / 388,843 B gzip, within
the unchanged ceilings.

Native picker activation/error feedback and cancellation handling retain the existing ceilings. Reusing the attachment action classes and interaction guard gives final totals of 1,404,916 B raw / 388,964 B gzip.

The shared Add menu now uses PortalKit’s body-mounted anchored popover, with panel-aware dismissal and keyboard handling. Keeping the existing synchronous file-input activation and removing redundant picker-error scaffolding gives 1,404,792 B raw / 388,923 B gzip within the unchanged ceilings. AgentKit style version 48 clears the previous inline-menu offsets on live hosts.

The Skills library split reuses the shared catalog with optional activation indicators. AgentKit style version 49 also hides WebKit’s native search-clear decoration alongside the custom clear action. Final total assets are 1,404,896 B raw / 388,958 B gzip, within the unchanged ceilings.

The ambiguous-upload retry fix retains uncommitted drafts until removal or scope cleanup. Final total assets are 1,404,892 B raw / 388,956 B gzip, within the unchanged ceilings; the YAML parser dependency is confined to Agents.


Extracting the conversation Running indicator into shared AgentKit replaces the
App Studio-only markup and keeps three staggered opacity pulses, reduced-motion
support, and live status semantics. Sizing and animation now come from canonical
AgentKit CSS so the indicator also works in providers without Tailwind utilities.
No library dependency or duplicated dependency closure is introduced. Total assets
measure 1,404,923 B raw / 388,995 B gzip, compared with 1,404,917 B raw / 388,996 B
gzip before extraction. All existing bundle ceilings remain unchanged. These are
local production bundle measurements, not browser timing evidence.


The shared conversation rail now renders its existing indeterminate spinner for
active runs as well as thread updates. Consolidating the busy-state predicate
keeps both the busy announcement and spinner consistent. Its fixed trailing slot
reserves title space and yields to hover/focus actions. No new runtime library
is introduced. With the current model-picker readiness changes, total assets
measure 1,405,384 B raw / 389,145 B gzip, compared with 1,404,617 B raw / 388,998 B
gzip before the thread spinner. The fixed total-only ceilings are 1,406,000 B raw
and 389,500 B gzip, leaving 355 B gzip headroom instead of relying on the previous
2 B margin. All path-specific limits remain unchanged.

### Shared command receipts

The command-output presentation adds copy feedback, optional line wrapping,
explicit preview expansion and invocation metadata in shared AgentKit. After
removing the obsolete nested output styles, the measured build is 1,199,450 B
raw / 325,179 B gzip on the page and 1,408,399 B raw / 389,861 B gzip total.
Against the preceding main build (1,405,366 B raw / 389,131 B gzip total), this
is 3,033 B raw / 730 B gzip of feature growth. No dependency was added; both
providers use the same canonical component and synchronized stylesheet.
Only the total ceiling increases, by 4,000 B raw / 1,000 B gzip, to account for
these controls. Bootstrap, dashboard, page and authentication limits stay fixed.
This is a local production-size measurement, not browser performance evidence.

### App Studio workbench

The workbench shares its document sections and request form, and Code and
Publishing adapt to the width of the pane. A same-toolchain build of the
preceding commit measured 1,409,496 B raw / 390,130 B gzip total. The workbench
with review fixes measures 1,410,195 B raw / 392,721 B gzip total and
1,200,993 B raw / 327,705 B gzip on the page path. The total limits increase
by 500 B raw and 2,500 B gzip to 1,410,500 B raw and 393,000 B gzip. The
path-specific limits retain their existing values.

### Shared command receipts

The production build for the shared AgentKit command receipt changes measured
1,410,858 B raw and 392,858 B gzip in both portal and container CI. The total
raw ceiling increases by 1,000 B to 1,411,500 B, leaving 642 B of raw headroom
on that build. The gzip total and path-specific ceilings retain their existing
values.
