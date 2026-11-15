---
{"schema":1,"id":"design.components.ai-action-row","title":"AI activity row","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AgentKit supplies the row; status and details come from the caller."},"appliesTo":["agents","app-studio"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-action-row.md#ai-activity-row","role":"design"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"action-row-canonical.test.ts","status":"passing","evidence":"Checks stable disclosure targets and lazy details in a mounted fixture."}]},"relatedDocuments":[{"id":"design.components.ai-conversation","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# AI activity row

## Purpose

Show a single ordered action and its reported state within an AI activity trace or run investigation.

## Use when

An agent or tool action belongs in a sequence where the user can inspect what happened. `AIActivityFeed` and `AITranscriptActivity` compose the shared row; providers supply the actual activities and interpret their status.

## Avoid when

Use an approval or interruption surface when a decision is required; a row alone does not grant authority. Keep essential failures and recovery visible rather than hiding them only in details. Do not claim success before a provider reports it.

## Anatomy and variants

1. **State cue:** an icon and accessible state label describe reported
   activity; busy, attention, failure, and canceled flags affect the cue.
2. **Title:** a concrete action name, optionally a disclosure button.
3. **Context:** an optional target, outcome, and caller-supplied trailing
   metadata clarify what happened.
4. **Details:** optional caller-provided evidence appears below the row when
   expanded.

`statusLabel` can supply a more precise accessible state. The row does not
interpret arguments or results.

## Behavior

Expandable rows emit `toggle`; the caller controls `expanded` and owns the details. The `details` slot contains caller-provided content. A disclosed result must not cause a tool to run again.

## Content

Use concrete action titles and truthful outcomes. Pair icon or color with meaningful text where the user needs to understand the state. Include a specific failure and recovery when known; omit sensitive arguments and results that the viewer is not authorized to see.

## Layout and responsive behavior

Rows keep a compact 28px minimum line height; titles and outcomes can truncate. Check the narrow frame and the actual product composition for long identifiers and expandable details.

## Accessibility

The state label is available to assistive technology. An expandable title is a button with `aria-expanded` and an optional `aria-controls`; its owner must supply a unique, stable details ID when exposing a controlled region. When a details slot and ID are supplied, the referenced container remains present while collapsed, but the details content is mounted only when expanded. Preserve visible focus and make status understandable without color alone. Screen-reader and cross-product behavior remain to be audited.

## Code and evidence

The design-book fixture mounts the canonical AgentKit activity row with
sample status and expandable evidence. The mounted test verifies that
`aria-controls` names an existing target when the row is collapsed and that
details mount only when opened.

## Related guidance

See [AI presentation](ai-conversation.md) and [accessible interaction](../accessibility/interaction.md).
