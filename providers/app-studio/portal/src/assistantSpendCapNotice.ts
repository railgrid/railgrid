import type { ProjectAssistantActionFeedItem } from './types'

export function shouldShowAssistantSpendCapChangesNotice(
  errorInfo: string | undefined,
  actionFeed: readonly ProjectAssistantActionFeedItem[] | undefined,
): boolean {
  return errorInfo === 'org_spend_cap_exceeded'
    && Boolean(actionFeed?.some((item) =>
      item.kind === 'edit'
      && item.status === 'succeeded'
      && item.groupKey === 'edit:files',
    ))
}
