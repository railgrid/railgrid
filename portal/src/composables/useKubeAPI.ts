import { ref, watchEffect, onUnmounted, type Ref } from 'vue'
import { useAuthStore } from '@/stores/auth'

const MCP_API_BASE = (cluster: string) =>
  `/clusters/${cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes`

const EDGE_API_BASE = (cluster: string) =>
  `/clusters/${cluster}/apis/kedge.faros.sh/v1alpha1/edges`

interface UseListResult<T> {
  data: Ref<T | null>
  error: Ref<string | null>
  loading: Ref<boolean>
  refetch: () => Promise<void>
}

async function kubeRequest(
  method: string,
  path: string,
  token: string,
  body?: unknown,
): Promise<Response> {
  const opts: RequestInit = {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
  }
  if (body) opts.body = JSON.stringify(body)
  return fetch(path, opts)
}

// --- Types ---

export interface KubernetesMCP {
  apiVersion: string
  kind: string
  metadata: {
    name: string
    creationTimestamp?: string
    uid?: string
    resourceVersion?: string
    labels?: Record<string, string>
  }
  spec: {
    edgeSelector?: {
      matchLabels?: Record<string, string>
      matchExpressions?: Array<{
        key: string
        operator: string
        values?: string[]
      }>
    }
    toolsets?: string[]
    readOnly?: boolean
  }
  status?: {
    URL?: string
    connectedEdges?: number
    conditions?: Array<{
      type: string
      status: string
      reason?: string
      message?: string
      lastTransitionTime?: string
    }>
  }
}

interface KubernetesMCPList {
  items: KubernetesMCP[]
}

// --- Composables ---

export function useMCPList(pollInterval?: number): UseListResult<KubernetesMCP[]> {
  const auth = useAuthStore()
  const data = ref<KubernetesMCP[] | null>(null) as Ref<KubernetesMCP[] | null>
  const error = ref<string | null>(null)
  const loading = ref(true)
  let timer: ReturnType<typeof setInterval> | null = null

  async function execute() {
    if (!auth.clusterName) {
      error.value = 'No cluster selected'
      loading.value = false
      return
    }
    try {
      loading.value = true
      error.value = null
      const token = await auth.getValidToken()
      const resp = await kubeRequest('GET', MCP_API_BASE(auth.clusterName), token)
      if (!resp.ok) {
        error.value = `Failed to list MCP servers: ${resp.status} ${resp.statusText}`
        return
      }
      const list: KubernetesMCPList = await resp.json()
      data.value = list.items ?? []
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Request failed'
    } finally {
      loading.value = false
    }
  }

  watchEffect(() => {
    if (auth.isAuthenticated) execute()
  })

  if (pollInterval && pollInterval > 0) {
    timer = setInterval(execute, pollInterval)
  }

  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })

  return { data, error, loading, refetch: execute }
}

export function useMCPGet(name: Ref<string>, pollInterval?: number): UseListResult<KubernetesMCP> {
  const auth = useAuthStore()
  const data = ref<KubernetesMCP | null>(null) as Ref<KubernetesMCP | null>
  const error = ref<string | null>(null)
  const loading = ref(true)
  let timer: ReturnType<typeof setInterval> | null = null

  async function execute() {
    if (!auth.clusterName || !name.value) {
      error.value = 'No cluster or name'
      loading.value = false
      return
    }
    try {
      loading.value = true
      error.value = null
      const token = await auth.getValidToken()
      const resp = await kubeRequest('GET', `${MCP_API_BASE(auth.clusterName)}/${name.value}`, token)
      if (!resp.ok) {
        error.value = `Failed to get MCP server: ${resp.status} ${resp.statusText}`
        return
      }
      data.value = await resp.json()
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Request failed'
    } finally {
      loading.value = false
    }
  }

  watchEffect(() => {
    if (auth.isAuthenticated && name.value) execute()
  })

  if (pollInterval && pollInterval > 0) {
    timer = setInterval(execute, pollInterval)
  }

  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })

  return { data, error, loading, refetch: execute }
}

export async function createMCP(spec: KubernetesMCP['spec'], name: string): Promise<KubernetesMCP> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const body = {
    apiVersion: 'mcp.kedge.faros.sh/v1alpha1',
    kind: 'Kubernetes',
    metadata: { name },
    spec,
  }
  const resp = await kubeRequest('POST', MCP_API_BASE(auth.clusterName), token, body)
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Create failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

export async function updateMCP(resource: KubernetesMCP): Promise<KubernetesMCP> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const resp = await kubeRequest(
    'PUT',
    `${MCP_API_BASE(auth.clusterName)}/${resource.metadata.name}`,
    token,
    resource,
  )
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Update failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

export async function deleteMCP(name: string): Promise<void> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const resp = await kubeRequest('DELETE', `${MCP_API_BASE(auth.clusterName)}/${name}`, token)
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Delete failed: ${resp.status} ${text}`)
  }
}

// --- Edge Types ---

export interface Edge {
  apiVersion: string
  kind: string
  metadata: {
    name: string
    creationTimestamp?: string
    uid?: string
    resourceVersion?: string
    labels?: Record<string, string>
  }
  spec: {
    type: string
  }
  status?: {
    joinToken?: string
    phase?: string
    connected?: boolean
    hostname?: string
    agentVersion?: string
  }
}

// --- Edge API ---

export async function createEdge(
  name: string,
  edgeType: string,
  labels?: Record<string, string>,
): Promise<Edge> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const body: Record<string, unknown> = {
    apiVersion: 'kedge.faros.sh/v1alpha1',
    kind: 'Edge',
    metadata: { name, ...(labels && Object.keys(labels).length > 0 ? { labels } : {}) },
    spec: { type: edgeType },
  }
  const resp = await kubeRequest('POST', EDGE_API_BASE(auth.clusterName), token, body)
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Create failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

export async function getEdge(name: string): Promise<Edge> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const resp = await kubeRequest('GET', `${EDGE_API_BASE(auth.clusterName)}/${name}`, token)
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Get failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

/** Poll the edge resource until status.joinToken is set, or timeout (30s). */
export async function pollEdgeJoinToken(name: string, timeoutMs = 30000): Promise<string> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const edge = await getEdge(name)
    if (edge.status?.joinToken) return edge.status.joinToken
    await new Promise((r) => setTimeout(r, 1000))
  }
  throw new Error('Timed out waiting for join token')
}

export async function deleteEdge(name: string): Promise<void> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()
  const resp = await kubeRequest('DELETE', `${EDGE_API_BASE(auth.clusterName)}/${name}`, token)
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Delete failed: ${resp.status} ${text}`)
  }
}
