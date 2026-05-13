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

export function useHubVersion() {
  // Kick off the fetch lazily; consumers read `hubVersion` reactively.
  void load()
  return { hubVersion }
}
