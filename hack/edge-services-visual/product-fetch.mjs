// Simulated responses for the product fixture; no request reaches a hub.
export function createFixtureFetch({ origin, isWorkloads, isWorkloadCreate, pageFailure, emptyRefresh, staleRefresh, slowRefresh, slowEmpty, firstRun, firstRunNoEdge, targets, now = Date.now }) {
  let deleted = false
  let attempts = 0
  let pageAttempts = 0
  let collectionReads = 0
  const started = now()
  let refreshFailed = false
return async (input, init) => {
  const url = new URL(String(input), origin)
  const path = url.pathname
  const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
  if ((slowRefresh || slowEmpty) && (path.endsWith('/services') || path.endsWith('/workloads')) && ++collectionReads > 1) {
    await new Promise(resolve => setTimeout(resolve, 1500))
    if (slowEmpty) return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: [] })
  }
  if (staleRefresh && !refreshFailed && now() - started >= 10_000 && (path.endsWith('/services') || path.endsWith('/workloads'))) {
    refreshFailed = true
    return json({ kind: 'Status', message: 'The collection could not be refreshed.' }, 503)
  }
  if (emptyRefresh && (path.endsWith('/services') || path.endsWith('/workloads'))) {
    if (!refreshFailed && now() - started >= 10_000) {
      refreshFailed = true
      return json({ kind: 'Status', message: 'The collection could not be refreshed.' }, 503)
    }
    return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: [] })
  }
  if ((firstRun || firstRunNoEdge) && (path.endsWith('/services') || path.endsWith('/workloads'))) return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: [] })
  if (pageFailure && (path.endsWith('/services') || path.endsWith('/workloads')) && url.searchParams.has('continue')) {
    if (++pageAttempts === 1) {
      await new Promise(resolve => setTimeout(resolve, 700))
      return json({ kind: 'Status', message: 'The next page could not be loaded.' }, 503)
    }
    return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: [] })
  }
  if (path.endsWith('/services/home-assistant') && init?.method === 'DELETE') {
    await new Promise(resolve => setTimeout(resolve, 700))
    if (++attempts === 1) return json({ kind: 'Status', message: 'The request was rejected.' }, 403)
    deleted = true
    return json({ kind: 'Status', status: 'Success' })
  }
  if (path.endsWith('/workloads/edge-proxy') && init?.method === 'DELETE') {
    await new Promise(resolve => setTimeout(resolve, 700))
    if (++attempts === 1) return json({ kind: 'Status', message: 'The request was rejected.' }, 403)
    deleted = true
    return json({ kind: 'Status', status: 'Success' })
  }
  if (path.endsWith('/services/home-assistant') && (!init?.method || init.method === 'GET') && !deleted) return json({ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'home-assistant' }, spec: { type: 'home-assistant', edgeRef: { kind: 'LinuxServer', name: 'home-server' }, port: 8123 }, status: { phase: 'Ready' } })
  if (path.endsWith('/workloads')) return json({ kind: 'WorkloadList', metadata: { resourceVersion: '1', ...(pageFailure ? { continue: 'next-page' } : {}) }, items: deleted ? [] : [{ metadata: { name: 'edge-proxy' }, spec: { targetNamespace: 'networking', simple: { image: 'registry.example.test/edge-proxy:v2' }, replicas: 1, placement: { strategy: 'Spread' } }, status: { phase: 'Running', readyReplicas: 1 } }] })
  if (path.endsWith('/services')) return json({ kind: 'ServiceList', metadata: { resourceVersion: '1', ...(pageFailure ? { continue: 'next-page' } : {}) }, items: deleted ? [] : [{ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Service', metadata: { name: 'home-assistant' }, spec: { type: 'home-assistant', edgeRef: { kind: 'LinuxServer', name: 'home-server' }, port: 8123 }, status: { phase: 'Ready' } }, ...(targets ? [
    { metadata: { name: 'lan-service' }, spec: { type: 'generic', edgeRef: { kind: 'LinuxServer', name: 'home-server' }, host: '192.168.1.5', port: 8080 } },
    { metadata: { name: 'cluster-service' }, spec: { type: 'generic', edgeRef: { kind: 'KubernetesCluster', name: 'review-cluster' }, targetRef: { namespace: 'apps', name: 'backend' }, port: 443 } },
    { metadata: { name: 'missing-cluster-target' }, spec: { type: 'generic', edgeRef: { kind: 'KubernetesCluster', name: 'review-cluster' }, port: 8080 } },
    { metadata: { name: 'unknown-edge-target' }, spec: { type: 'generic', edgeRef: { name: 'unknown' }, port: 8080 } },
  ] : [])] })
  if (path.endsWith('/linuxservers')) return json({ kind: 'LinuxServerList', metadata: { resourceVersion: '1' }, items: firstRunNoEdge && !isWorkloads ? [] : [{ metadata: { name: 'home-server' }, spec: {}, status: { phase: 'Ready', connected: !emptyRefresh } }] })
  if (path.endsWith('/kubernetesclusters')) return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: isWorkloadCreate || (isWorkloads && firstRun) ? [{ metadata: { name: 'review-cluster', labels: { env: 'dev' } }, spec: {}, status: { phase: 'Ready', connected: true } }] : [] })
  if (path.endsWith('/macosservers')) return json({ kind: 'List', metadata: { resourceVersion: '1' }, items: [] })
  return json({ kind: 'Status', message: `Fixture did not mock ${path}` }, 404)
}

}
