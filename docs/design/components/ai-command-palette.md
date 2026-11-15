---
{"schema":1,"id":"design.components.ai-command-palette","title":"Composer command palette","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AgentKit supplies contextual command discovery; callers own available commands, resource discovery, selection, and authority."},"appliesTo":["agents","app-studio","agentkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-command-palette.md#composer-command-palette","role":"design"},{"path":"provider-sdk/agentkit-vue/AICommandPalette.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationComposer.vue","role":"implementation"},{"path":"providers/agents/portal/src/AgentRichComposer.vue","role":"reference"},{"path":"providers/app-studio/portal/src/AssistantCommandPalette.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"Agents palette interaction, accessibility, and lifecycle tests","status":"passing","evidence":"Mounted tests cover keyboard selection, focus ownership, dismissal, and stale resource discovery."}]},"relatedDocuments":[{"id":"design.components.ai-conversation","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Composer command palette

## Purpose

Help a user find a command, attach an enabled skill, or reference a resource while composing a turn.

## Use when

The choice belongs to the current conversation and can be supplied by the caller. The shared rich composer uses this palette for slash discovery; providers supply commands, enabled skills, resource discovery, and selection state.

## Avoid when

The user needs application-wide navigation or a global command search. This palette is scoped to the composer and does not grant access to a skill or resource. Enabling a skill is a separate product action.

## Anatomy and variants

The initial command list can lead to a searchable skills list or a provider list and resource results. Each view provides contextual labels, a Back control when nested, and an empty, loading, or warning state as appropriate. Selected or unavailable entries cannot be selected again; the caller owns resulting chips and their removal.

## Behavior

Arrow keys move through available options and Enter selects. Escape returns to the previous view or closes the palette; dismissal restores the invoking focus unless the user has deliberately moved focus elsewhere. Resource requests that resolve after navigation or dismissal must not replace the current view. A failed lookup displays a warning and can be retried by returning to the provider.

## Content

Name each action and resource in terms of what it adds to the turn. Empty and failure messages distinguish no match, no available resource, and an unsuccessful lookup. Never imply that selecting context grants new authority.

## Layout and responsive behavior

On desktop the palette is anchored to its invoking control and constrained to the viewport. On narrow screens it uses a bounded panel. Long results must remain discoverable without obscuring the current selection or dismiss controls.

## Accessibility

The listbox and its options have instance-owned IDs. Preserve visible focus, an accessible active option, keyboard navigation, and 44px coarse-pointer targets. When command discovery retains editor focus, its temporary combobox attributes are restored on dismissal. A persistent polite status outside the listbox announces loading and settled empty results as the view changes; visible copies are hidden from assistive technology to avoid duplicate announcements. Warning text remains available to assistive technology.

## Code and evidence

The canonical implementation is [`AICommandPalette.vue`](../../../provider-sdk/agentkit-vue/AICommandPalette.vue), composed by [`AIConversationComposer.vue`](../../../provider-sdk/agentkit-vue/AIConversationComposer.vue). The Agents [composer](../../../providers/agents/portal/src/AgentRichComposer.vue) and App Studio [adapter](../../../providers/app-studio/portal/src/AssistantCommandPalette.vue) provide product context. The design review mounts the shared composer with sample skills; the [Agents product fixture](http://127.0.0.1:5198/?provider=agents&scenario=palette&route=%23%2Fagents%2Fresearch-assistant%2Fchat) mounts the actual chat with a sample session and enabled skill. At 390px in light and dark themes, the product fixture opened the skill list, selected the sample by keyboard, returned focus to the composer, and dismissed the command list with Escape. Neither fixture exercises live provider discovery or authorization; this check does not establish screen-reader coverage.

A Chromium check of the shared composer specimen at 1440px and 390px in both
themes also exercised slash entry, skill selection, Shift+Tab to Back, Escape
through the views, and dismissal when focus deliberately left the palette.
It found no document overflow. The queued-focus race is covered by a mounted
regression test; this browser check does not prove that timing path or live data.

## Related guidance

See [AI conversation](ai-conversation.md) for composer ownership and [accessible interaction](../accessibility/interaction.md) for focus and touch rules.
