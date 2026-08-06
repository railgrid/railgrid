import type { ProjectAssistantSkill } from './types'

export function filterAssistantSkills(
  skills: readonly ProjectAssistantSkill[],
  query: string,
): ProjectAssistantSkill[] {
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
