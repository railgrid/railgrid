---
{"schema":1,"id":"design.components.model-reasoning-picker","title":"Model and reasoning picker","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AgentKit presents model and discrete effort choices; providers own capabilities, persistence, and recovery."},"appliesTo":["agents","app-studio","agentkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/model-reasoning-picker.md#model-and-reasoning-picker","role":"design"},{"path":"provider-sdk/agentkit-vue/AIModelReasoningPicker.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentChat.vue","role":"reference"},{"path":"providers/app-studio/portal/src/App.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"App Studio reasoning picker mounted tests","status":"passing","evidence":"Mounted tests exercise model choice, effort, keyboard, cancellation, and persistence boundaries."}]},"relatedDocuments":[{"id":"design.components.model-connections","relation":"see-also"},{"id":"design.components.ai-conversation","relation":"see-also"},{"id":"design.patterns.controls","relation":"see-also"}]}
---

# Model and reasoning picker

## Purpose

Let a user choose a configured model and, when supported, a discrete reasoning effort for a conversation.

## Use when

The caller has model-specific capability and saved preference information. Both Agents and App Studio supply those values and own the resulting persistence.

## Avoid when

The task is configuring a provider connection, adding a model, or choosing a generic numeric value. Use the appropriate [model setup](model-connections.md) or [control pattern](../patterns/controls.md).

## Anatomy and variants

The compact or regular trigger opens an anchored panel. The effort view contains the current model, a discrete range with labeled stops, and reset. Model default is the first stop; explicit choices follow. A nested model list shows configured models and their workspace-default designation. Models without supported efforts retain model selection without a range.

## Behavior

Moving the range previews a stop; committing emits the choice to the caller. Canceling a pointer gesture does not commit the preview. Reset selects the model default. Model selection returns to the effort view; the caller owns whether a change saves, fails, or needs retry. Pending saves avoid competing changes. Invalid saved choices must show recovery rather than implying a new choice was saved.

## Content

Use the caller-provided effort labels and default; do not infer model capability. Explain a missing or unsupported setting and offer the relevant retry, reset, or model-settings action when the caller supplies it.

## Layout and responsive behavior

The popup is anchored to its trigger and constrained to the viewport. Keep labels readable and controls reachable at narrow widths; the nested list remains within the same popup.

## Accessibility

The range is a native input with a name, bounds, step, and textual value. Arrow keys, Home, and End follow native range behavior. The model list supports keyboard navigation; Escape returns from the list to the effort view, then closes the popup and restores trigger focus. Pending and invalid states remain understandable without color alone.

## Code and evidence

The canonical component is [`AIModelReasoningPicker.vue`](../../../provider-sdk/agentkit-vue/AIModelReasoningPicker.vue). Agents uses it in [chat](../../../providers/agents/portal/src/views/AgentChat.vue); App Studio uses it in its [composer](../../../providers/app-studio/portal/src/App.vue). The [Agents fixture](http://127.0.0.1:5198/?provider=agents&scenario=palette&route=%23%2Fagents%2Fresearch-assistant%2Fchat) supplies a sample model and in-memory preferences. The [App Studio project fixture](http://127.0.0.1:5198/?provider=app&scenario=app-picker&theme=light&route=rollout-console) supplies two sample models, one with reasoning choices. Neither contacts a model service; the App Studio scenario rejects writes.

In a 390px browser viewport with touch emulation, the Agents fixture's trigger,
model-choice row, and back button measured at least 44px high; the back button
was also 44px wide. The open panel fit within the viewport. Escape returned
from the model list to the effort view, then closed the panel and restored
focus to the trigger. This is fixture evidence, not a physical-device or live
service check; the separate `AIModelPicker` remains unverified by this review.

At 320px in the dark-theme Agents fixture, the open effort and model-list
panels each fit within 8px viewport gutters without horizontal overflow. The
range, list option, and back control measured 44px high under touch emulation.

At 390px with touch emulation in the light-theme App Studio project fixture,
the picker trigger and both model rows measured 44px high, and the panel fit
without horizontal overflow. Selecting the second model returned to the view
without effort choices. This checks the simulated project, not persistence or a
live model service.

## Related guidance

See [model connections](model-connections.md), [AI conversation](ai-conversation.md), and [control patterns](../patterns/controls.md).
