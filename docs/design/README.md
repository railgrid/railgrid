# Railgrid design knowledge base

Violet Circuit is Railgrid's shared visual constitution: light by default with a
first-class dark theme, sharp, dense, mono-heavy, and lit only where a state is
alive. This directory is the
browsable authority. Use the navigation order below to distinguish an existing
shared contract from composition guidance, vocabulary, and feature precedent.

## Authority and navigation order

1. Start with the canonical shared implementation and component contracts for
   a surface that already exists. [Components](components/) describe reusable
   UI contracts and their current implementation state, including every
   distributed PortalKit asset.
2. Use [patterns](patterns/) for page, read, creation, navigation, and form
   composition across components.
3. Use [foundations](foundations.md) and its linked entries for the shared
   token, theme, type, geometry, icon, and integration vocabulary.
4. Consult feature-specific precedent last. A provider or route may show how a
   contract is applied, but does not become cross-product authority by example.

Metadata decides the strength and maturity of every claim: `authority` records
design intent and implementation ownership; `status` records document life
cycle; `implementation.state` records whether the surface is planned, partial,
or shipped; and `verification` records current evidence. Normative design text
does not by itself prove a shipped implementation, and partial or unverified
guidance must not be reported as globally shipped.

## Taxonomy

- [Foundations](foundations.md) is the short system map.
- Foundations comprise [principles](foundations/principles.md),
  [color and materials](foundations/colors.md), [typography](foundations/typography.md),
  [spacing and shape](foundations/geometry.md), [iconography](foundations/iconography.md),
  and [motion](foundations/motion.md).
- Implementation guidance covers [theme mechanics](implementation/theming.md), [provider integration](implementation/provider-integration.md),
  and [shared CSS recipes](implementation/recipes.md).
- [Components](components/) document reusable UI contracts and their current
  implementation status, including every distributed PortalKit asset.
- [Patterns](patterns/) document page, read, creation, navigation, and form
  composition that cuts across components.
- [AI UX](ai/) documents conversation, autonomy, run, and evidence/status
  contracts, plus explicitly unshipped gaps.
- [Content](content/) documents product-language and UI-copy contracts with
  their implementation evidence.
- [Accessibility](accessibility/) documents interaction and accessibility
  contracts with their implementation evidence.
- [Quality](quality/) documents review, conformance, exceptions, and known
  oddities.

Every catalog entry has a stable `id`. The machine-readable contract is
[schema.md](schema.md), and new entries start from the [general template](templates/design-document.md)
or the [component template](templates/component-document.md). Run `make verify-design-docs`
to validate frontmatter, source links, related IDs, Markdown links, and stale
numeric PortalKit or AgentKit style-version claims; use
`node hack/verify-design-docs.mjs --catalog` to inspect the deterministic
catalog. [docs/design-book.md](../design-book.md) remains a physical
compatibility pointer for old links and is not a second source of rules.
