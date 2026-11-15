---
{
  "schema": 1,
  "id": "design.foundations.system",
  "title": "Violet Circuit system map",
  "kind": "system",
  "status": "active",
  "authority": { "design": "normative", "implementation": "canonical" },
  "implementation": {
    "state": "shipped",
    "notes": "The host stylesheet, PortalKit recipes, and provider integration surfaces are shipped; individual contracts record their own verification evidence."
  },
  "appliesTo": ["portal", "provider-portals", "portalkit", "dex"],
  "owner": "design-system",
  "canonicalSource": [
    { "path": "docs/design/foundations.md#violet-circuit-system-map", "role": "design" },
    { "path": "portal/src/assets/main.css", "role": "implementation" },
    { "path": "provider-sdk/portalkit/railgrid-ui.css", "role": "implementation" }
  ],
  "verification": {
    "state": "verified",
    "checks": [
      { "kind": "command", "ref": "make verify-design-docs", "status": "passing" },
      { "kind": "command", "ref": "make verify-ui-conformance", "status": "passing" }
    ]
  },
  "relatedDocuments": [
    { "id": "design.foundations.principles", "relation": "see-also", "path": "docs/design/foundations/principles.md" },
    { "id": "design.quality.review-checklist", "relation": "see-also", "path": "docs/design/quality/review-checklist.md" },
    { "id": "design.foundations.provider-integration", "relation": "see-also", "path": "docs/design/implementation/provider-integration.md" },
    { "id": "design.foundations.theming", "relation": "see-also", "path": "docs/design/implementation/theming.md" },
    { "id": "design.quality.exceptions", "relation": "see-also", "path": "docs/design/quality/exceptions.md" }
  ]
}
---

# Violet Circuit system map

Foundations describe how Railgrid looks and feels:

1. [Principles](foundations/principles.md) — the design decisions that guide the system.
2. [Color and materials](foundations/colors.md) — the palette and the surfaces it creates.
3. [Typography](foundations/typography.md) — typefaces and the hierarchy of text.
4. [Spacing and shape](foundations/geometry.md) — relationships between elements and their geometry.
5. [Iconography](foundations/iconography.md) — the shared visual vocabulary for icons.
6. [Motion](foundations/motion.md) — transitions, activity, and reduced motion.

The material roles and spacing observations remain open for review; grouping
them here does not make them new enforced tokens. [Components](components/)
and [patterns](patterns/) show how the foundations are used in tasks.
Implementation details such as [theme mechanics](implementation/theming.md), [provider integration](implementation/provider-integration.md),
and [CSS recipes](implementation/recipes.md) live in developer guidance.

The host portal and every provider micro-frontend share one visual language.
Provider implementation status is intentionally explicit in each entry's
metadata so a planned build-to-this specification cannot be mistaken for
runtime evidence.
