import { fmtUSD, type UsageBucket, type UsagePoint } from './types'

type CostUsage = Pick<UsageBucket | UsagePoint, 'runs' | 'inputTokens' | 'outputTokens' | 'usdMicros' | 'unpricedRuns'>

/** Older providers omit coverage. Treat metered zero-cost usage as unknown. */
export function unpricedRunCount(usage: CostUsage): number {
  if (typeof usage.unpricedRuns === 'number') return Math.max(0, usage.unpricedRuns)
  return usage.usdMicros === 0 && usage.inputTokens + usage.outputTokens > 0 ? usage.runs : 0
}

export function usageCostLabel(usage: CostUsage): string {
  return usage.usdMicros === 0 && unpricedRunCount(usage) > 0 ? 'Unknown' : fmtUSD(usage.usdMicros)
}
