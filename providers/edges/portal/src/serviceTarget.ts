import type { EdgeService } from './types'

// A blank target denotes loopback only on host edges. Kubernetes and unknown
// edge kinds do not establish where the service is reachable.
export function serviceTarget(service: EdgeService): string {
  const host = service.host?.trim()
  const name = service.targetName?.trim()
  const target = host || (name ? `${service.targetNamespace ? `${service.targetNamespace}/` : ''}${name}` : '')
  if (target) return service.port ? `${target}:${service.port}` : target
  if (service.edgeKind === 'LinuxServer' || service.edgeKind === 'MacOSServer') {
    return service.port ? `Agent loopback:${service.port}` : 'Agent loopback'
  }
  return 'Target not specified'
}
