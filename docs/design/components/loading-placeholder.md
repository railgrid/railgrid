---
{"schema":1,"id":"design.components.loading-placeholder","title":"Loading placeholder","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"ResourcePage and ResourceTable provide layout-aware placeholders; there is no standalone universal skeleton component."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/loading-placeholder.md#loading-placeholder","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/useDelayedLoading.ts","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Resource page and table loading specimens","status":"pending"}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also"},{"id":"design.components.resource-table","relation":"see-also"},{"id":"design.patterns.resource-reads","relation":"see-also"},{"id":"design.foundations.motion","relation":"see-also"}]}
---

# Loading placeholder

## Purpose

Maintain a useful page shape during an initial read without presenting placeholders as real data.

## Use when

A page or table has no authoritative snapshot yet and its expected layout is known. `ResourcePage` supplies collection and detail fallbacks; its loading slot can represent a more specific known layout. `ResourceTable` supplies a table-shaped first-read placeholder.

## Avoid when

A request has failed, a settled result is empty, or a previous snapshot is available. Show the appropriate error or empty state. During a background refresh retain the previous rows or page, announce the update, and identify stale data if refresh fails. A skeleton does not represent individual values or promise an eventual result.

## Anatomy and variants

1. **Stable shell:** retain the page heading and expected content boundaries.
2. **Visual shapes:** collection placeholders suggest rows; detail placeholders
   suggest sections and, where applicable, summary cards. Table shapes follow
   visible columns. Bars are not labels or controls.
3. **Loading status:** the shell supplies a delayed, named announcement
   separately from decorative shapes.

A resource-specific loading slot may use a more accurate composition without
inventing data. There is no standalone universal skeleton component.

## Behavior

The pending layout mounts immediately but its visible and announced loading treatment is delayed by 180ms, so a quick read does not flash. The caller owns the read and decides whether the result is loaded, empty, or failed. The shell owns the loading status; replacing the visual loading slot must preserve that ownership.

## Content

Name the resource being loaded in the status where known. Do not use plausible resource names, counts, or actions as placeholder content. A refresh or retry should say what is happening, and an error should provide a recovery path.

Do not label metadata unavailable merely because the first read is pending.
For example, Infrastructure's instance heading waits for the instance snapshot
before showing its template name or an unavailable value.

## Layout and responsive behavior

Follow the shape and spacing of the eventual page or table without promising an exact number of results. Preserve the normal page header and responsive table behavior. Review the final layout beside the pending layout at the same width.

## Accessibility

Placeholder shapes are hidden from assistive technology. The live status sits
outside the busy subtree, while staying in the active dialog when the shell is
mounted inside one. Keep placeholders unfocusable and respect reduced motion.

## Code and evidence

Open the [loading review](http://127.0.0.1:5197/?entry=design.components.loading-placeholder)
to compare first load, loaded data, failed initial read, and background refresh
for a deployment detail, collection, and searchable table. The fixture drives
the shared components with synthetic data. Mounted checks cover the delay and
announcement. Screen-reader announcements are pending manual review.

## Related guidance

See [resource reads](../patterns/resource-reads.md), [ResourcePage](resource-page.md), [ResourceTable](resource-table.md), and [motion](../foundations/motion.md).
