# Model connection rendered audit

Durable browser measurements and check results are in [browser evidence](browser-evidence.json). References below to `/tmp/railgrid-ui-audit/` are supplemental machine-local logs and captures.

Final Models collection captures are [light](models-light.png) and [dark](models-dark.png).

Scope: the real Agents and App Studio provider custom-element entry points, under the host portal reset, tokens and self-hosted Instrument Sans, Archivo and IBM Plex Mono. Matrix: light/dark × 1440px/390px × OpenAI/custom × both providers (16 unmasked captures).

## Fixture repair

- Fixed a missing opening brace on the provider frame selector in `hack/models-form-visual/fixture.css`.
- Replaced retired provider REST mocks with workspace-bound Studio and ModelCredential Kubernetes resources, current collections, and published current OAuth/catalog/verb paths in `fixture.js`.
- The fake host uses the actual `userId` authority identity and normalizes the host-owned subPath. No real API key, paid request, external model call, discovery, test, or save is performed.
- README now describes actual fixture scope and provider-specific differences.

## Verification contract

The repaired first run produced all 16 images with real fonts loaded, zero browser errors and no horizontal overflow, but its historic exact-zero cross-provider PNG gate failed. That gate compared complete forms whose provider-owned credential storage help, supported providers, custom transport description, prerequisite notices and recommendations intentionally differ. Those semantics were retained.

The authoritative runner now independently gates the normative design book contract: single fluid wide creation root; explicit 14px/21px route typography; Archivo at 125%, 18px heading; token 2px heading focus ring; fixed 14px back SVG; header and initial-control geometry shared across providers; correct section hierarchy; labeled controls with resolvable help; provider/endpoint desktop top alignment and mobile stack; 40px desktop/44px mobile field heights; coarse-pointer action targets; no horizontal overflow; loaded fonts; no console/page errors; empty new draft with explicit provider and disabled discovery/test/connect. It independently exercises provider/endpoint changes after each untouched screenshot, asserting typed credential clearing and zero saved-resource writes.

All complete raw screenshots and exact, unmasked PNG-difference images/counts/channel metrics remain in the JSON report for inspection. Differences are observational; no pixel tolerance, masking, copy normalization, or source-override styling was introduced. Shared header and initial-control geometry remains an exact assertion. A stacked mobile endpoint follows provider-owned help, so only that endpoint's y position is excluded from cross-provider equality; each form's local stacking contract and matching x/width/height remain gated.

The final `make test-model-form-visual` passes (exit 0): all 16 captures pass normative visual/accessibility/geometry and local draft-behavior checks, and all 8 exact shared-geometry comparisons pass. The previous run caught a real App Studio endpoint-change credential-retention defect; its owner corrected the handler and this final run confirms both adapters clear typed keys when either provider or endpoint changes. All 16 cases perform zero saved-resource writes, load the real fonts, emit zero console/page errors and have no horizontal overflow. Unmasked image differences are 13.7–25.6% and remain observational. Evidence directory `/tmp/railgrid-ui-audit/model-forms`, JSON `models-form-visual-regression.json`. No mocked browser check establishes live credential validation or comprehensive screen-reader compliance.
