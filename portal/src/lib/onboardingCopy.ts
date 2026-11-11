export const audienceCopy = [
  { id: 'team', title: 'A platform for my team', body: 'Choose the tools your team can use in this Workspace.' },
  { id: 'personal', title: 'Build and run projects', body: 'Use this Workspace’s tools for apps, agents, and services.' },
  { id: 'explore', title: 'I’m evaluating Railgrid', body: 'Find a useful first workflow to try.' },
] as const

export const goalCopy = [
  { id: 'app-studio', title: 'Build an app', body: 'Develop an application with AI in App Studio.', setupLabel: 'App Studio', setupAccess: 'These tools get access to workspace resources for app development. Organization access for sharing apps can be reviewed later in Providers.', next: 'Open App Studio', task: 'Describe your app', prerequisite: 'App Studio guides you through model and project setup.' },
  { id: 'agents', title: 'Run an AI agent', body: 'Create an AI agent with tools and memory.', setupLabel: 'Agents', setupAccess: 'Setup grants workspace access so Agents can use connected services and reach agent runners on your hosts.', next: 'Open Agents', task: 'Configure an agent', prerequisite: 'Connect a model and choose its tools before running a task.' },
  { id: 'infrastructure', title: 'Deploy a service', body: 'Start with a configurable application template.', setupLabel: 'Application Templates', setupAccess: 'Application Templates can read credentials labeled for its use and check access to deployments in this Workspace.', next: 'Open Application Templates', task: 'Choose a service template', prerequisite: 'Have a deployment target and your template’s configuration ready.' },
  { id: 'edges', title: 'Connect infrastructure', body: 'Reach your servers and clusters through Railgrid.', setupLabel: 'Edges', setupAccess: 'Edges can verify agent connections and manage credentials labeled for its use. After setup, open Edges to connect a server or cluster.', next: 'Open Edges', task: 'Connect a server or cluster', prerequisite: 'You’ll need permission to install the Railgrid agent on your server or cluster.' },
] as const

const teamGoalCopy: Record<string, { title: string; body: string }> = {
  'app-studio': { title: 'Build apps with AI', body: 'Give your team App Studio for developing applications.' },
  agents: { title: 'Run AI agents', body: 'Let your team create agents with tools and memory.' },
  infrastructure: { title: 'Deploy applications', body: 'Provide ready-to-configure application templates.' },
  edges: { title: 'Connect servers and clusters', body: 'Bring your team’s infrastructure into Railgrid.' },
}

const providerPurpose: Record<string, string> = {
  'app-studio': 'Build applications with AI.',
  agents: 'Run AI agents with tools and memory.',
  infrastructure: 'Provision apps and infrastructure from templates.',
  edges: 'Connect servers and Kubernetes clusters.',
  code: 'Connect and manage source repositories.',
}

export function defaultGoalForAudience(audience: string): string {
  return audience === 'team' ? 'edges' : audience === 'explore' ? 'agents' : 'app-studio'
}

export function goalTitleForAudience(goal: string, team: boolean): string {
  return team ? teamGoalCopy[goal]?.title ?? '' : goalCopy.find(option => option.id === goal)?.title ?? ''
}

export function goalBodyForAudience(goal: string, team: boolean): string {
  return team ? teamGoalCopy[goal]?.body ?? '' : goalCopy.find(option => option.id === goal)?.body ?? ''
}

export function purposeForProvider(goal: string, name: string, description = ''): string {
  if (goal === 'app-studio' && name === 'code') return 'Manage source repositories for your apps.'
  if (goal === 'app-studio' && name === 'infrastructure') return 'Provide the runtime for your apps.'
  if (goal === 'agents' && name === 'infrastructure') return 'Run services that agents can use as tools.'
  if (goal === 'agents' && name === 'edges') return 'Connect agents to services and runners on your hosts.'
  return providerPurpose[name] || description
}
