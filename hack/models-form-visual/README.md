# Models form visual fixture

This fixture mounts the real App Studio and Agents provider entry
points in one Vite document. It supplies the host `railgridContext`, tenant
storage, and deterministic bound Kubernetes resources used by the visual comparator.
Model configuration comes from the workspace Studio plus its provider
connections, and from Agents ModelCredential plus ModelProviderConnection
objects. The fixture does not mock retired provider REST APIs or use real
credentials.
The stylesheet imports the portal's real `main.css` and scans the provider
sources so utility classes are compiled in the external fixture root.

From the repository root, with the existing App Studio portal dependencies and
the root portal Fontsource dependencies installed, start the fixture with the
manual Make target:

```sh
make serve-model-form-visual MODEL_FORM_FONT_NODE_MODULES="$PWD/portal/node_modules"
```

In another shell, run the comparator with the Playwright module supplied by
the browser tooling already available in the environment:

```sh
PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs \
  MODEL_FORM_BROWSER_EXECUTABLE=/path/to/chromium \
  make test-model-form-visual MODEL_FORM_OUTPUT=/tmp/models-form-visual
```

The comparator writes raw screenshots, unmasked PNG diffs, and a computed
geometry/style report to `MODEL_FORM_OUTPUT` (or its temporary default). It
captures the initial OpenAI state and a `custom` state reached by changing the
real provider control for App Studio and Agents. It also captures App Studio's
Google Vertex service-account mode at desktop and mobile sizes in both themes.
Agents has no service-account credential mode, so that variant is not applicable
to its form. The Vertex textarea is left empty; the fixture makes no Google
requests and uses no real credentials. Screenshot values are not filled,
normalized, masked, or cropped to hide differences.

Both actual create routes now open on the provider-connection stage. The initial
draft has no internal resource name or model binding. App Studio exposes an
optional connection display label before verification. Setup does not depend on an
existing configured model or model Secret. The fixture supplies the new bound
`ModelProviderConnection` resource shape for each provider; App Studio's Studio
starts with an empty model registry. After each untouched screenshot, the
comparator makes only in-memory provider and endpoint edits to confirm a typed
key is cleared and no resource write occurs.

The gate checks the contracts in `docs/design/components/model-connections.md`:
the fluid creation root and explicit typography, the shared header and back
icon, route heading focus, named provider and credential fields with help,
40px desktop and 44px mobile controls, mobile action targets, loaded host fonts,
browser errors, and horizontal overflow. It confirms that discovery and model
verification wait until the provider stage is complete. The shared-geometry
checks compare actual route landmarks and controls; no screenshot pixels are
masked, normalized, or cropped to hide provider differences. The synthetic
destination edits do not invoke discovery, testing, or saving. API behavior is
covered by `make test-model-connections` and `make test-model-connections-api`.

Cross-provider PNG differences remain complete review evidence with exact pixel
counts and channel differences; they are observational and do not determine the
exit code. App Studio supports Google credential variants and recommendations;
Agents offers additional chat providers and harness identities. Their provider
guidance and storage copy remain visible in the captures. The geometry gate
compares the shared route frame, provider controls, credential controls, and the
custom endpoint target without modifying the screenshots.

## App Studio Setup review matrix

With the fixture server running, capture the two App Studio Setup tabs:

```sh
PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs \
  MODEL_FORM_BROWSER_EXECUTABLE=/path/to/chromium \
  MODEL_FORM_OUTPUT=/tmp/models-tabs-review \
  node hack/models-tabs-visual.mjs
```

`models-tabs-visual.mjs` captures Models and Usage & cost at 1440px and 390px
in light and dark themes (eight screenshots). It checks both collections on
Models, model expansion, the Create menu and provider form, the existing
provider connections link, tab selection, overflow, and browser errors.
`index.html` is the local evidence gallery and `report.json` records the matrix.
The opt-in `scenario=tabs` data supplies a configured model and shared provider;
Usage & cost shows its actual reporting-unavailable state. This captures the
provider shell, not the host console's workspace sidebar. The existing create-form
matrix keeps its empty registry and does not use this scenario.
