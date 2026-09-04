---
{"schema":1,"id":"design.quality.review-checklist","title":"Violet Circuit review checklist","kind":"policy","status":"active","authority":{"design":"normative","implementation":"none"},"implementation":{"state":"not-applicable","notes":"Reviewers apply this checklist to every UI change; it is a design contract, not a runtime component."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/quality/review-checklist.md#violet-circuit-review-checklist","role":"design"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"design review","status":"not-run"}]},"relatedDocuments":[{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
---

# Violet Circuit review checklist

Before merging any UI change:

- [ ] No raw hex/rgb outside the [sanctioned exceptions](exceptions.md); no
      dead Precision Flat accents.
- [ ] Visible labels and state copy follow the [UI copy policy](../content/ui-copy.md):
      truthful domain verbs, actionable object labels, distinct loading/empty/
      failure/retry copy, product-first detail, and no secret values.
- [ ] Interaction follows the [accessible interaction policy](../accessibility/interaction.md):
      native semantics and names, keyboard/focus paths, live read-state
      announcements, coarse-pointer targets, both-theme contrast for
      portal/provider surfaces (Dex is fixed dark), and reduced motion; Kuery
      graph colors remain an unvalidated exception.
- [ ] No new `border-radius` outside 2, 3, 4, 6, 8, or 12px and true circles;
      no pills.
- [ ] Badges are square mono tags and status maps to semantic tone.
- [ ] Exactly the sanctioned things glow; danger never glows.
- [ ] Provider-level route/section tabs use PortalKit `Tabs`, caller-owned
      routing, `aria-current="page"`, and no tab glow or shadow.
- [ ] Portal/provider surfaces work in both themes; toggle them rather than
      trusting the default. Dex auth is the fixed-dark standalone exception.
- [ ] Shared `k-*` and PortalKit primitives are used instead of re-derived
      markup.
- [ ] Ordinary routes use the fluid `AppLayout` column without a competing
      page-level `max-w-*`; prose, simple forms/search, and dense forms follow
      the local readability rules.
- [ ] Creation surface matches the task: focused and route-owned for
      independently managed resources, contextual for compact parent-dependent
      additions. Route-owned flows use the canonical skeleton and substantial
      forms do not reflow collection pages.
- [ ] Identifiers use mono and aligned digits use tabular numerals.
- [ ] Icons are Lucide or PortalKit `ic()`; no emoji or Unicode glyph icons;
      stroke and size follow the iconography law and only status icons carry
      color.
- [ ] `prefers-reduced-motion` is respected for every new animation.
