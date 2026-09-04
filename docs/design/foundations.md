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
    { "path": "provider-sdk/portalkit/faros-ui.css", "role": "implementation" }
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
    { "id": "design.foundations.provider-integration", "relation": "see-also", "path": "docs/design/foundations/provider-integration.md" },
    { "id": "design.foundations.theming", "relation": "see-also", "path": "docs/design/foundations/theming.md" },
    { "id": "design.quality.exceptions", "relation": "see-also", "path": "docs/design/quality/exceptions.md" }
  ]
}
---

# Violet Circuit system map

The system is one contract with several layers. [Principles](foundations/principles.md)
state what must remain true. [Tokens and geometry](foundations/colors.md) and
[typography](foundations/typography.md) provide the vocabulary. [Recipes](foundations/recipes.md)
and [components](components/) provide reusable implementation contracts.
[Patterns](patterns/) explain how components compose into complete journeys.
[Quality](quality/) is the review and conformance boundary.

The host portal and every provider micro-frontend share one visual language.
Provider implementation status is intentionally explicit in each entry's
metadata so a planned build-to-this specification cannot be mistaken for
runtime evidence.
