import { ref, watchEffect, onUnmounted, type Ref } from 'vue'
import { createGraphQLClient } from '@/graphql/client'
import { useAuthStore } from '@/stores/auth'
import { useTenantStore } from '@/stores/tenant'
import type { CombinedError } from '@urql/vue'

// Window event the shell listens for to drop a dead session and bounce
// to /login (see portal/src/App.vue). Decoupled from a direct
// `@/router` import on purpose: this composable is reused by provider
// micro-frontends via the `@kedge-edges` alias, and a static
// `import { router } from '@/router'` dragged the ENTIRE portal SPA
// (every route page → AppLayout → TerminalDock) into each provider's
// IIFE bundle. That bundled TerminalDock's terminal-session store got
// shadowed by the provider's dispatch-only terminal-adapter, killing
// the SSH-terminal bridge in production builds. A window event keeps the
// shell's redirect behaviour while staying a no-op inside providers
// (they register no listener).
export const SESSION_EXPIRED_EVENT = 'kedge-session-expired'

interface UseQueryResult<T> {
  data: Ref<T | null>
  error: Ref<string | null>
  loading: Ref<boolean>
  refetch: () => Promise<void>
}

// handleSessionFailure converts a dead gateway session into a clean
// redirect to the login page instead of surfacing a raw urql
// "[Network] ... 404 page not found" string.
//
// A transport-level 401/403 means the bearer token was rejected; a 404
// on /graphql/{cluster} means the route is gone — typically a hard
// reload restoring a stale/destroyed clusterName from localStorage (the
// static-token reload case), or a workspace that no longer exists. In
// every case the persisted session can no longer serve the app, so we
// drop it and bounce to /login.
//
// Returns true when it handled the error (caller should stop). Skipped
// while the tenant store is still provisioning a first-login control
// plane: 404s are expected there (the cluster isn't serving yet) and the
// ControlPlaneProvisioning overlay already covers the UI, so we must not
// log the user out mid-bootstrap.
function handleSessionFailure(err: CombinedError | undefined): boolean {
  if (!err?.networkError) return false
  const status = (err.response as Response | undefined)?.status
  if (status !== 401 && status !== 403 && status !== 404) return false

  const tenant = useTenantStore()
  if (tenant.bootstrapState === 'provisioning') return false

  // Hand the actual logout + redirect to the shell (App.vue) so this
  // composable never has to touch `@/router` or assume a portal-shaped
  // auth store — see SESSION_EXPIRED_EVENT above. No-op inside provider
  // micro-frontends, where a dead query shouldn't tear down the host.
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))
  return true
}

export function useGraphQLQuery<T>(
  query: string,
  variables?: Record<string, unknown>,
  pollInterval?: number,
): UseQueryResult<T> {
  const auth = useAuthStore()
  const data = ref<T | null>(null) as Ref<T | null>
  const error = ref<string | null>(null)
  const loading = ref(true)

  let timer: ReturnType<typeof setInterval> | null = null

  async function execute() {
    if (!auth.clusterName) {
      error.value = 'No cluster selected'
      loading.value = false
      return
    }

    const client = createGraphQLClient(auth.clusterName, () => auth.getValidToken())

    try {
      loading.value = true
      error.value = null
      const result = await client
        .query(query, variables ?? {})
        .toPromise()

      if (result.error) {
        if (handleSessionFailure(result.error)) return
        error.value = result.error.message
      } else {
        data.value = result.data as T
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Query failed'
    } finally {
      loading.value = false
    }
  }

  watchEffect(() => {
    if (auth.isAuthenticated) {
      execute()
    }
  })

  if (pollInterval && pollInterval > 0) {
    timer = setInterval(execute, pollInterval)
  }

  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })

  return { data, error, loading, refetch: execute }
}

export async function graphqlMutate<T = unknown>(
  mutation: string,
  variables: Record<string, unknown>,
): Promise<T> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')

  const client = createGraphQLClient(auth.clusterName, () => auth.getValidToken())
  const result = await client.mutation(mutation, variables).toPromise()

  if (result.error) {
    if (handleSessionFailure(result.error)) {
      throw new Error('Session expired')
    }
    throw new Error(result.error.message)
  }

  return result.data as T
}

// graphqlQuery runs a one-off query (as opposed to the reactive
// useGraphQLQuery composable). Use it when the variables aren't known at
// setup time — e.g. fetching a Secret whose name comes from another
// resource's status.
export async function graphqlQuery<T = unknown>(
  query: string,
  variables: Record<string, unknown>,
): Promise<T> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')

  const client = createGraphQLClient(auth.clusterName, () => auth.getValidToken())
  const result = await client.query(query, variables).toPromise()

  if (result.error) {
    if (handleSessionFailure(result.error)) {
      throw new Error('Session expired')
    }
    throw new Error(result.error.message)
  }

  return result.data as T
}
