import { ref } from 'vue'
import { fetchVersion } from '@/lib/api'
import type { VersionResponse } from '@/auth/types'

const hubVersion = ref<VersionResponse | null>(null)
let inflight: Promise<VersionResponse | null> | null = null

async function load(): Promise<VersionResponse | null> {
  if (hubVersion.value) return hubVersion.value
  if (inflight) return inflight
  inflight = fetchVersion()
    .then((v) => {
      hubVersion.value = v
      return v
    })
    .catch(() => null)
    .finally(() => {
      inflight = null
    })
  return inflight
}

// isAgentOutdated returns true when both versions are known, neither is the
// placeholder "dev" build, and the agent's version differs from the hub's.
export function isAgentOutdated(agentVersion: string | undefined, hubVer: string | undefined): boolean {
  if (!agentVersion || !hubVer) return false
  if (agentVersion === 'dev' || hubVer === 'dev') return false
  return agentVersion !== hubVer
}

// hubVersionLabel renders the hub build the way provider versions are shown
// ("v1.2.3"): release builds get a "v" prefix unless they already carry one;
// "dev" and commit-ish builds are shown as-is.
export function hubVersionLabel(v: VersionResponse | null): string {
  const version = v?.version?.trim()
  if (!version) return ''
  return /^\d+\.\d+/.test(version) ? `v${version}` : version
}

// hubVersionDetails is the tooltip text: commit and build date when known.
export function hubVersionDetails(v: VersionResponse | null): string {
  if (!v) return ''
  const parts = [`Platform ${hubVersionLabel(v)}`]
  if (v.gitCommit && v.gitCommit !== 'unknown') parts.push(`commit ${v.gitCommit}`)
  if (v.buildDate && v.buildDate !== 'unknown') parts.push(`built ${v.buildDate}`)
  return parts.join(' · ')
}

export function useHubVersion() {
  // Kick off the fetch lazily; consumers read `hubVersion` reactively.
  void load()
  return { hubVersion }
}
