# Models form visual fixture

This fixture mounts the real App Studio and Agents provider entry
points in one Vite document. It supplies the host `railgridContext`, tenant
storage, and deterministic bound Kubernetes resources used by the visual comparator.
Model configuration comes from the workspace Studio and ModelCredential objects;
the fixture does not mock retired provider REST APIs or use real credentials.
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
  make test-model-form-visual MODEL_FORM_OUTPUT=/tmp/models-form-visual
```

The comparator writes raw screenshots, unmasked PNG diffs, and a computed
geometry/style report to `MODEL_FORM_OUTPUT` (or its temporary default). It
captures both the initial OpenAI state and a `custom` state reached by changing
the real provider control. Screenshot values are not filled, normalized, masked,
or cropped to hide differences.

The gate checks the contracts in `docs/design/components/model-connections.md`:
the fluid creation root and explicit typography, the shared header and back
icon, the route's heading focus, section hierarchy, named fields and help,
aligned desktop Connection controls, stacked mobile controls, 40px desktop and
44px mobile field sizes, touch action targets, loaded host fonts, browser errors,
and horizontal overflow. It also checks the new draft starts empty with an
explicit provider choice and disabled discovery/test/save actions. After the
screenshot, synthetic local edits verify that changing a provider or endpoint
clears the typed credential and never writes saved resources. These edits do
not invoke discovery, testing, or saving; API verification remains covered by
`make test-model-connections` and `make test-model-connections-api`.

Cross-provider PNG differences remain complete review evidence with exact pixel
counts and channel differences; they are observational and do not determine the
exit code. App Studio supports Google credential variants and recommendations;
Agents supports additional chat providers and harness identities. Credential
storage guidance and test-prerequisite notices reflect each adapter's behavior.
Custom endpoint guidance also describes their different supported transports.
Those capabilities and copy legitimately change section and footer height.
The check compares shared header and initial control geometry separately, and
checks each form's alignment and field sizes without a pixel tolerance. A mobile
endpoint follows the provider's wrapped help, so its vertical position may
vary while its width and height must match.
