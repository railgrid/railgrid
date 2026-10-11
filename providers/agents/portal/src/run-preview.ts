// A run's input can be a whole prompt — the API bounds spec.inputPreview at
// 2 KiB, and a webhook-fired review brief uses most of it. The activity feed
// shows one line of it: whitespace collapsed and cut at a word boundary, in a
// width-capped column so a long prompt truncates with disclosure instead of
// widening the table past the viewport. The run page shows the input in full.

/** Characters shown in the row itself. */
export const RUN_PREVIEW_CELL_CHARS = 200
/** Characters offered by the truncation disclosure on hover or focus. */
export const RUN_PREVIEW_DISCLOSURE_CHARS = 600
/** Width cap for the run column: about one sentence, never most of a phone. */
export const RUN_PREVIEW_MAX_WIDTH = 'min(64ch, 55vw)'

export function runPreview(value: string | undefined, max: number): string {
  const collapsed = (value ?? '').replace(/\s+/g, ' ').trim()
  if (collapsed.length <= max) return collapsed
  const cut = collapsed.slice(0, max)
  const space = cut.lastIndexOf(' ')
  return `${(space > max / 2 ? cut.slice(0, space) : cut).trimEnd()}…`
}
