import { useAuthStore } from '@/stores/auth'

const VW_NS_API_BASE = (cluster: string, namespace: string) =>
  `/clusters/${cluster}/apis/kedge.faros.sh/v1alpha1/namespaces/${namespace}/virtualworkloads`

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

// --- Create spec ---

export interface VirtualWorkloadCreateSpec {
  name: string
  namespace: string
  image: string
  replicas: number
  containerPort?: number
  env?: Array<{ name: string; value: string }>
  edgeSelector?: Record<string, string>
  strategy?: string // "Spread" | "Singleton"
  expose?: boolean
  dnsName?: string
  accessPort?: number
}

// --- CRUD ---

export async function createVirtualWorkload(spec: VirtualWorkloadCreateSpec): Promise<unknown> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()

  const simple: Record<string, unknown> = {
    image: spec.image,
  }
  if (spec.containerPort) {
    simple.ports = [{ containerPort: spec.containerPort, protocol: 'TCP' }]
  }
  if (spec.env && spec.env.length > 0) {
    simple.env = spec.env.filter((e) => e.name.trim())
  }

  const placement: Record<string, unknown> = {
    strategy: spec.strategy || 'Singleton',
  }
  if (spec.edgeSelector && Object.keys(spec.edgeSelector).length > 0) {
    placement.edgeSelector = { matchLabels: spec.edgeSelector }
  }

  const body: Record<string, unknown> = {
    apiVersion: 'kedge.faros.sh/v1alpha1',
    kind: 'VirtualWorkload',
    metadata: {
      name: spec.name,
      namespace: spec.namespace,
    },
    spec: {
      simple,
      replicas: spec.replicas,
      placement,
      ...(spec.expose || spec.dnsName || spec.accessPort
        ? {
            access: {
              ...(spec.expose ? { expose: true } : {}),
              ...(spec.dnsName ? { dnsName: spec.dnsName } : {}),
              ...(spec.accessPort ? { port: spec.accessPort } : {}),
            },
          }
        : {}),
    },
  }

  const resp = await kubeRequest(
    'POST',
    VW_NS_API_BASE(auth.clusterName, spec.namespace),
    token,
    body,
  )
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Create failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

export async function scaleVirtualWorkload(
  name: string,
  namespace: string,
  replicas: number,
): Promise<unknown> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()

  // Get current, patch replicas
  const getResp = await kubeRequest(
    'GET',
    `${VW_NS_API_BASE(auth.clusterName, namespace)}/${name}`,
    token,
  )
  if (!getResp.ok) {
    const text = await getResp.text()
    throw new Error(`Get failed: ${getResp.status} ${text}`)
  }
  const vw = await getResp.json()
  vw.spec.replicas = replicas

  const resp = await kubeRequest(
    'PUT',
    `${VW_NS_API_BASE(auth.clusterName, namespace)}/${name}`,
    token,
    vw,
  )
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Scale failed: ${resp.status} ${text}`)
  }
  return resp.json()
}

export async function deleteVirtualWorkload(
  name: string,
  namespace: string,
): Promise<void> {
  const auth = useAuthStore()
  if (!auth.clusterName) throw new Error('No cluster selected')
  const token = await auth.getValidToken()

  const resp = await kubeRequest(
    'DELETE',
    `${VW_NS_API_BASE(auth.clusterName, namespace)}/${name}`,
    token,
  )
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`Delete failed: ${resp.status} ${text}`)
  }
}
