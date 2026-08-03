import type { ProjectAssistantActionFeedItem } from './types'
import type { AssistantProgress } from './assistantProgress'

export type AssistantTraceBlock =
  | {
      kind: 'progress'
      key: string
      message: string
    }
  | {
      kind: 'actions'
      key: string
      items: ProjectAssistantActionFeedItem[]
    }

interface OrderedProgress {
  kind: 'progress'
  index: number
  sequence: number
  message: string
}

interface OrderedAction {
  kind: 'action'
  index: number
  sequence: number
  item: ProjectAssistantActionFeedItem
}

type OrderedTraceItem = OrderedProgress | OrderedAction

export function buildAssistantTrace(
  progress: AssistantProgress,
  actions: ProjectAssistantActionFeedItem[],
): AssistantTraceBlock[] {
  const ordered: OrderedTraceItem[] = [
    ...progress.messages.map((message, index): OrderedProgress => ({
      kind: 'progress',
      index,
      sequence: progress.messageSequences[index],
      message,
    })),
    ...actions.map((item, index): OrderedAction => ({
      kind: 'action',
      index,
      sequence: item.sequence,
      item,
    })),
  ]
  ordered.sort(compareTraceItems)

  const blocks: AssistantTraceBlock[] = []
  for (const event of ordered) {
    if (event.kind === 'progress') {
      blocks.push({
        kind: 'progress',
        key: `progress-${event.index}`,
        message: event.message,
      })
      continue
    }
    const previous = blocks[blocks.length - 1]
    if (previous?.kind === 'actions') {
      previous.items.push(event.item)
      continue
    }
    blocks.push({
      kind: 'actions',
      key: `actions-${event.index}`,
      items: [event.item],
    })
  }
  return blocks
}

function compareTraceItems(left: OrderedTraceItem, right: OrderedTraceItem): number {
  if (left.sequence !== right.sequence) {
    return left.sequence - right.sequence
  }
  if (left.kind !== right.kind) return left.kind === 'action' ? -1 : 1
  return left.index - right.index
}
