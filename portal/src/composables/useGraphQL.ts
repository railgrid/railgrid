import { ref, watchEffect, onUnmounted, type Ref } from 'vue'
import { createGraphQLClient } from '@/graphql/client'
import { useAuthStore } from '@/stores/auth'

interface UseQueryResult<T> {
  data: Ref<T | null>
  error: Ref<string | null>
  loading: Ref<boolean>
  refetch: () => Promise<void>
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
