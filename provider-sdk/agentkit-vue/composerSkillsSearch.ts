import type { AgentKitSkill } from './composer'

export function filterAssistantSkills(
  skills: readonly AgentKitSkill[],
  query: string,
): AgentKitSkill[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (!terms.length) return [...skills]
  return skills.filter((skill) => {
    const searchable = [
      skill.name,
      skill.description,
      skill.id,
      skill.packageName,
      skill.scope,
    ].filter(Boolean).join(' ').toLowerCase()
    return terms.every((term) => searchable.includes(term))
  })
}
