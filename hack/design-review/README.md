# Review the Railgrid design book

Start the local fixture with the portal dependencies installed:

```sh
make serve-design-review
```

Open http://127.0.0.1:5197/. The fixture is a local review aid and does not
require a hub, credentials, or a model. It has three views per entry:

- **Fixture:** a mounted component with sample data, a canonical CSS specimen,
  or a labeled inspection study. Use the theme selector and the optional
  narrow browser frame. Use a touch device or browser emulation for pointer
  and reduced-motion checks.
- **Contract:** the design book entry, section links, and its implementation
  and verification boundaries. Click the line number to read the Markdown
  source.
- **Sources:** canonical metadata references, an excerpt viewer, recorded
  evidence, and related entries. Fixture-specific references open directly
  at a recipe, declaration, or implementation location.

The asset list reads the sync manifest. An asset that is absent from the
book’s table still has a manifest location, a source file, and related
guidance. The token and recipe inventories, contract body, and exception
locators are also read from their owning files. Run
`node hack/design-review/catalog.mjs` to check the coverage and locators.

Start with system and token rules, then work through shared controls,
resource reads, creation, and the AI experience. Policy, open specifications,
and integration boundaries include inspection examples and links. No fixture
proves live backend behavior, production rollout, OAuth, assistive-technology
conformance, or the deployment status of an unshipped design.

To review a built version, run
`portal/node_modules/.bin/vite build --config hack/design-review/vite.config.mjs`
and serve `.kcp/design-review-dist/` on localhost. The separate Impeccable
scan preview has its own entry point at http://127.0.0.1:5195/.
