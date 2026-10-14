import { normalizePageResource, type PublishPageResource } from '../portalkit/page-context'

// This pilot supports infrastructure Instance pages only. Extend this explicit
// contract when another provider adds a reviewed page-resource publisher.
export function createPageResourcePublisher(
  provider: string,
  isCurrent: () => boolean,
  publish: PublishPageResource,
): PublishPageResource | undefined {
  if (provider !== 'infrastructure') return undefined
  return value => {
    if (!isCurrent()) return
    if (value === null) { publish(null); return }
    const resource = normalizePageResource(value)
    if (!resource || resource.apiVersion !== 'infrastructure.railgrid.ai/v1alpha1' ||
      resource.kind !== 'Instance' || resource.resource !== 'instances' ||
      (resource.readTool !== undefined && resource.readTool !== 'get_instance')) { publish(null); return }
    publish(resource)
  }
}
