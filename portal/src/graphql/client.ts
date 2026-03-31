import { createClient, fetchExchange, mapExchange } from '@urql/vue'
import { API_PATHS } from '@/lib/constants'

export function createGraphQLClient(
  clusterName: string,
  getToken: () => Promise<string>,
) {
  const authExchange = mapExchange({
    async onOperation(operation) {
      const token = await getToken()
      return {
        ...operation,
        context: {
          ...operation.context,
          fetchOptions: {
            ...(operation.context.fetchOptions as RequestInit | undefined),
            headers: {
              ...((operation.context.fetchOptions as RequestInit | undefined)?.headers),
              Authorization: `Bearer ${token}`,
            },
          },
        },
      }
    },
  })

  return createClient({
    url: API_PATHS.graphql(clusterName),
    exchanges: [authExchange, fetchExchange],
  })
}
