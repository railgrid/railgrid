import type { Resource } from './api';

export function registeredTeams(teams: Resource[], connection: Resource): Resource[] {
  return teams.filter(team => !team.metadata.deletionTimestamp && team.spec?.connection === connection.metadata.name && team.spec?.connectionUID === connection.metadata.uid);
}

export function teamIDs(teams: Resource[]): string[] {
  return [...new Set(teams.map(team => String(team.spec?.teamID)))].sort();
}
