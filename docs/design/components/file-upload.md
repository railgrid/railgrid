---
{"schema":1,"id":"design.components.file-upload","title":"File upload and dropzone","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/file-upload.md","role":"design"},{"path":"provider-sdk/agentkit-vue/AIConversationComposer.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"hack/design-review/attachment-product.test.ts","status":"passing","evidence":"Mounted Agents composer fixture checks retry focus and a pending non-image upload with the reduced-motion class; CSS generation was checked in the App Studio production bundle."},{"kind":"browser","ref":"Desktop and mobile upload and retry","status":"pending","evidence":"The mounted Agents fixture was checked with a synthetic pending text attachment: computed animation was none under emulated reduced motion and spin under normal motion; uploading text remained visible. Native file selection, retry and touch drag have not been verified in a browser."},{"kind":"review","ref":"Product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# File upload and dropzone

## Purpose

Let a user choose or drop supported files.

## Use when

A user must supply a file. Offer a standalone target for a focused upload, or a contextual target inside the conversation composer.

## Avoid when

The user is choosing an existing Railgrid resource or providing a short value; use the corresponding picker or input instead.

## Anatomy and variants

1. **Entry:** a labeled standalone picker/drop target, or the composer's Add
   menu and contextual drop target.
2. **Constraints:** accepted types and relevant limits, available before or at
   selection.
3. **Selected files:** a recognizable name and state for each file, with
   removal and retry where applicable.
4. **Feedback:** a specific validation or upload error beside the affected
   file or target.

The current composer accepts up to eight attachments per turn, totaling at
most 50 MiB: supported images up to 8 MiB, supported text up to 1 MiB, and
other files up to 25 MiB. These are client limits, not a claim about every
backing service.

## Behavior

Provide a file-picker alternative to dragging. The standalone text-only
specimen accepts nonempty `.txt` files and reports an unsupported extension or
empty file beside the target. It checks the filename and size, not file
contents; each real upload surface must state its own
restrictions. The Agents and App Studio composer accepts any file type. It
classifies PNG, JPEG, and WebP by MIME type, or by extension when the browser
supplies an empty or generic binary MIME type. It classifies `.txt` and `.md`
as text only when their MIME type is plain text, Markdown, empty, or generic
binary. Other combinations are file attachments represented to the model by
metadata; a familiar extension alone does not guarantee content handling. The
receipt's content type and size also determine whether it can be projected as
image or text content. Reject empty files and files
beyond the applicable limits with a local error. Allow retry or removal after
an upload failure; do not treat a failed or pending upload as ready to send.
Do not imply an upload occurred in a static specimen.

Removal is also an asynchronous operation when a file has a server receipt.
Keep its identity and a pending state visible until removal settles; if it
fails, identify the failure and offer **Retry removal** rather than implying
the file was removed or allowing the unresolved attachment to be sent. A
file still uploading can be cancelled, but cancellation or cleanup is not
proof that remote bytes were deleted. Do not promise deletion until the
service confirms it.

Keep each upload and removal associated with the conversation and draft that
started it. A late result after a conversation, owner, or draft change must not
add a file, error, or success state to the new context. Cleanup of an abandoned
upload may be best effort; do not present it as confirmed deletion.

Pasting into the shared conversation composer can also create an attachment.
The first clipboard image takes precedence over clipboard text. Otherwise,
plain text of at most 10 KiB in UTF-8 stays inline; text over that boundary is
submitted as `pasted-text.txt` through the attachment path. The size is bytes,
not characters. The usual text-file and aggregate limits, upload state, and
recovery rules still apply; a large paste is not automatically ready to send.
App Studio's pre-project composer stages the same clipboard inputs for later
upload rather than uploading them immediately. Preserve a visible attachment
state so a user can distinguish staged or pending content from inline text.

## Content

Name the action **Choose a file** or **Add files**, and state the accepted
types, limits, and any material processing distinction near the entry point.
An error should identify the affected file or constraint and say what the
user can do next. Avoid implying that an attached file's contents are analyzed
when only its metadata is available to the model.

## Layout and responsive behavior

The standalone target occupies its task area; the composer keeps attachment
controls within the existing message surface. Let file names truncate visually
only when their full names remain available to assistive technology, and allow
file chips and actions to wrap rather than widen the page. Keep error, retry,
and removal controls adjacent to the file. Review both themes and the 390px
frame.

## Accessibility

Use a keyboard-operable picker, expose accepted types and errors, and do not
require drag gestures. Name retry and removal controls for the affected file.
Upload and removal indicators respect reduced-motion preferences. If retry
removes the focused action, return focus to the composer without stealing it
from a user who has already moved elsewhere.

## Code and evidence

The [shared composer](../../../provider-sdk/agentkit-vue/AIConversationComposer.vue),
[attachment validation](../../../provider-sdk/agentkit-vue/composerAttachments.ts),
and [Agents wrapper](../../../providers/agents/portal/src/AgentRichComposer.vue)
implement the contextual example. The [file-upload review](http://127.0.0.1:5197/?entry=design.components.file-upload)
mounts the Agents composer with a simulated upload service and a separate
standalone text-only specimen. The App Studio wrapper uses the shared
composer. These examples do not establish end-to-end backend behavior or
product-wide adoption. An earlier browser review at 390px recorded a synthetic
text attachment failing and retrying successfully, focus returning to the
composer, and no page overflow. That scoped observation does not establish
current browser verification: the broader desktop and mobile upload and retry
review remains pending. The mounted test exercises retry focus and a pending
synthetic text attachment; native picker, touch drag, and live service behavior
remain unverified.

The shared composer distinguishes upload and removal errors and retries the
corresponding operation. It fences late upload results by owner and draft and
attempts cleanup for abandoned uploads. This is source evidence for that
composer, not a live-service or product-wide deletion guarantee.

The paste boundary and image precedence are source evidence from the shared
composer and [App Studio's pre-project composer](../../../providers/app-studio/portal/src/AssistantPreProjectComposer.vue).
They have not been independently verified with native clipboard input in a
browser or across assistive technologies.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
