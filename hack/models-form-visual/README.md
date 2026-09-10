# Models form visual fixture

This fixture mounts the real App Studio, Agents, and Databricks provider entry
points in one Vite document. It supplies the host `farosContext`, tenant
storage, and deterministic empty API responses used by the visual comparator.
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
the real provider control; no values are filled or normalized.
