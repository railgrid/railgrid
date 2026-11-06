import { createKubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { providerFetch } from './portalkit/tenant'
import { parseSkillDocument, validateSkillResources } from './skill-document'
import type { AgentSkillCatalog, AgentSkillDetail, RailgridContext } from './types'

export const WORKSPACE_SKILLS: KubeResourceRef = { group: 'agents.railgrid.ai', version: 'v1alpha1', resource: 'skills' }
interface SkillObject extends KubeObject { spec?: { document?: string; resources?: Array<{ name: string; content: string }> } }
interface PublishedProvider {
  name: string
  hub?: { assistantSkills?: Array<{ packageName: string; version?: string; digest?: string; skill: string; resources?: Array<{ path: string; content: string }> }> }
}

function client(context: RailgridContext | null) {
  if (!context?.tenant) throw new Error('Select a workspace to browse skills.')
  return createKubeClient({ fetch: providerFetch(context), cluster: context.tenant })
}

/** Library keys identify source packages; runtime qualification and activation
 * remain exclusively in the authorized per-Agent catalog. */
export function librarySkill(scope: 'workspace' | 'system', packagePath: string, document: string, resources: Array<{ name: string; content: string }>, version?: string, digest?: string): AgentSkillDetail {
  const parsed = parseSkillDocument(document)
  const checked = validateSkillResources(document, resources)
  return {
    id: `${scope}:${packagePath}`, name: parsed.name, description: parsed.description,
    scope, packagePath, packageName: packagePath, enabled: false, editable: scope === 'workspace',
    version, digest, instructions: parsed.body, document,
    resources: checked.map(resource => ({ path: resource.name, content: resource.content, size: new TextEncoder().encode(resource.content).byteLength })),
  }
}

async function providers(context: RailgridContext | null): Promise<PublishedProvider[]> {
  const response = await providerFetch(context)('/api/providers')
  if (!response.ok) throw new Error('Could not load published provider skills.')
  return (await response.json() as { items?: PublishedProvider[] }).items || []
}

export async function loadSkillLibrary(context: RailgridContext | null): Promise<AgentSkillCatalog> {
  const [workspace, published] = await Promise.allSettled([client(context).listAll<SkillObject>(WORKSPACE_SKILLS), providers(context)])
  const skills: AgentSkillDetail[] = [], warnings: string[] = []
  if (workspace.status === 'rejected' && published.status === 'rejected') throw workspace.reason
  if (workspace.status === 'fulfilled') {
    for (const item of workspace.value) {
      try { skills.push(librarySkill('workspace', item.metadata.name, item.spec?.document || '', item.spec?.resources || [], item.metadata.resourceVersion)) }
      catch { warnings.push(`Workspace skill “${item.metadata.name}” is invalid or exceeds its bounds.`) }
    }
  } else warnings.push('Workspace skills could not be loaded. Retry to refresh the library.')
  if (published.status === 'fulfilled') {
    for (const provider of published.value) for (const item of provider.hub?.assistantSkills || []) {
      try { skills.push(librarySkill('system', `providers/${provider.name}/${item.packageName}`, item.skill, (item.resources || []).map(resource => ({ name: resource.path, content: resource.content })), item.version, item.digest)) }
      catch { warnings.push(`Published skill “${provider.name}/${item.packageName}” could not be read.`) }
    }
  } else warnings.push('Provider skills could not be loaded. Retry to refresh the library.')
  return { skills, warnings }
}

export async function loadSkillLibraryDetail(context: RailgridContext | null, id: string): Promise<AgentSkillDetail> {
  if (id.startsWith('workspace:')) {
    const item = await client(context).get<SkillObject>(WORKSPACE_SKILLS, id.slice('workspace:'.length))
    return librarySkill('workspace', item.metadata.name, item.spec?.document || '', item.spec?.resources || [], item.metadata.resourceVersion)
  }
  const catalog = await loadSkillLibrary(context)
  const skill = catalog.skills.find(item => item.id === id)
  if (!skill) throw new Error('This skill is no longer in the library. Refresh the catalog.')
  return skill as AgentSkillDetail
}
