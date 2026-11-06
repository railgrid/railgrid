// Resuming a parked harness requires a nonempty resolution. This instruction
// communicates the skip without supplying a product decision.
export const skippedQuestionResponse = 'The user skipped this question without answering. Continue with the task where possible. If the missing decision prevents progress, explain what remains blocked.'

export interface QuestionPresentation {
  prompt: string
  options: { label: string; description: string }[]
}

/** Codex's runner renders a single structured question with this Options block.
 * Keep arbitrary prose and multi-question prompts intact for a text answer. */
export function questionPresentation(value?: string): QuestionPresentation {
  const prompt = value?.trim() || 'The agent is waiting for an answer.'
  const [heading, choices, extra] = prompt.split('\nOptions:\n')
  const plain = { prompt, options: [] }
  if (extra !== undefined || !choices || !heading || heading.includes('\n')) return plain
  const rows = choices.split('\n')
  if (rows.length < 2 || rows.length > 8) return plain
  const options: QuestionPresentation['options'] = []
  for (const row of rows) {
    const match = /^- ([^:]+): (.+)$/.exec(row)
    if (!match) return plain
    const label = match[1].trim()
    const description = match[2].trim()
    if (!label || !description || options.some(option => option.label === label)) return plain
    options.push({ label, description })
  }
  return { prompt: heading, options }
}

export function answeredQuestionCount(answers: import('./types').QuestionAnswers): number {
  return Object.values(answers).filter(answer => answer !== null).length
}
