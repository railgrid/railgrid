// Shared mutation helpers. Each performs one API write, sets a note, then
// reloads the affected collection (which re-renders). Both the menu views and
// the flow view call these so the reference-patch semantics live in one place.

import type { ViewCtx } from './view'

// ---- agents ----------------------------------------------------------------

export async function createAgent(vc: ViewCtx, name: string, modelCredential?: string): Promise<boolean> {
  try {
    const body: Record<string, unknown> = { name, displayName: name }
    if (modelCredential) body.modelCredential = modelCredential
    await vc.api.send('POST', '/api/agents', body)
    await vc.store.loadAgents()
    return true
  } catch (e) {
    vc.notify('Create failed: ' + (e as Error).message)
    return false
  }
}

export async function deleteAgent(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/agents/${encodeURIComponent(name)}`)
    await vc.store.loadAgents()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

export async function updateAgent(vc: ViewCtx, name: string, patch: Record<string, unknown>, note = 'Saved.'): Promise<void> {
  try {
    await vc.api.send('PUT', `/api/agents/${encodeURIComponent(name)}`, patch)
    vc.notify(note)
    await vc.store.loadAgents()
  } catch (e) {
    vc.notify('Save failed: ' + (e as Error).message)
  }
}

// ---- credentials -----------------------------------------------------------

export async function createCredential(vc: ViewCtx, body: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('POST', '/api/credentials', body)
    vc.notify('Credential saved.')
    await vc.store.loadCredentials()
    return true
  } catch (e) {
    vc.notify('Save failed: ' + (e as Error).message)
    return false
  }
}

export async function deleteCredential(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/credentials/${encodeURIComponent(name)}`)
    await vc.store.loadCredentials()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

// ---- connections -----------------------------------------------------------

export async function createConnection(vc: ViewCtx, body: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('POST', '/api/connections', body)
    vc.notify('Connection created.')
    await vc.store.loadConnections()
    return true
  } catch (e) {
    vc.notify('Create failed: ' + (e as Error).message)
    return false
  }
}

export async function updateConnection(vc: ViewCtx, name: string, patch: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('PUT', `/api/connections/${encodeURIComponent(name)}`, patch)
    vc.notify('Connection updated.')
    await vc.store.loadConnections()
    return true
  } catch (e) {
    vc.notify('Update failed: ' + (e as Error).message)
    return false
  }
}

export async function deleteConnection(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/connections/${encodeURIComponent(name)}`)
    await vc.store.loadConnections()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

export async function testConnection(vc: ViewCtx, name: string): Promise<void> {
  vc.notify(`Testing ${name}…`)
  try {
    await vc.api.send('POST', `/api/connections/${encodeURIComponent(name)}/test`)
    vc.notify(`Test message sent via ${name}. Check the channel.`)
  } catch (e) {
    vc.notify(`Test failed: ${(e as Error).message}`)
  }
}

export async function oauthConnect(vc: ViewCtx, name: string): Promise<void> {
  try {
    const res = await vc.api.send<{ authorizeURL: string }>('POST', `/api/connections/${encodeURIComponent(name)}/oauth/authorize`, {
      publicBaseURL: location.origin,
    })
    window.open(res.authorizeURL, '_blank', 'noopener')
    vc.notify('Authorize in the opened tab, then refresh.')
  } catch (e) {
    vc.notify(`OAuth connect failed: ${(e as Error).message}`)
  }
}

export async function enableInbound(vc: ViewCtx, name: string): Promise<void> {
  vc.notify(`Enabling inbound for ${name}…`)
  try {
    const res = await vc.api.send<{ webhookURL: string; registered: boolean; note: string }>('POST', `/api/connections/${encodeURIComponent(name)}/enable-inbound`, {
      publicBaseURL: location.origin,
    })
    vc.notify(`${res.registered ? '✅' : 'ℹ️'} ${res.note} URL: ${res.webhookURL}`)
    await vc.store.loadConnections()
  } catch (e) {
    vc.notify(`Enable inbound failed: ${(e as Error).message}`)
  }
}

// ---- toolsets --------------------------------------------------------------

export async function createToolset(vc: ViewCtx, body: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('POST', '/api/toolsets', body)
    vc.notify('Toolset created.')
    await vc.store.loadToolsets()
    return true
  } catch (e) {
    vc.notify('Create failed: ' + (e as Error).message)
    return false
  }
}

export async function updateToolset(vc: ViewCtx, name: string, patch: Record<string, unknown>): Promise<void> {
  try {
    await vc.api.send('PUT', `/api/toolsets/${encodeURIComponent(name)}`, patch)
    vc.notify('Toolset updated.')
    await vc.store.loadToolsets()
  } catch (e) {
    vc.notify('Update failed: ' + (e as Error).message)
  }
}

export async function deleteToolset(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/toolsets/${encodeURIComponent(name)}`)
    await vc.store.loadToolsets()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

// ---- schedules -------------------------------------------------------------

export async function createSchedule(vc: ViewCtx, body: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('POST', '/api/schedules', body)
    vc.notify('Schedule created.')
    await vc.store.loadSchedules()
    return true
  } catch (e) {
    vc.notify('Create failed: ' + (e as Error).message)
    return false
  }
}

export async function updateSchedule(vc: ViewCtx, name: string, patch: Record<string, unknown>, note = 'Schedule updated.'): Promise<void> {
  try {
    await vc.api.send('PUT', `/api/schedules/${encodeURIComponent(name)}`, patch)
    vc.notify(note)
    await vc.store.loadSchedules()
  } catch (e) {
    vc.notify('Update failed: ' + (e as Error).message)
  }
}

export async function deleteSchedule(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/schedules/${encodeURIComponent(name)}`)
    await vc.store.loadSchedules()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

export async function runSchedule(vc: ViewCtx, name: string): Promise<void> {
  vc.notify(`Running ${name}…`)
  try {
    const res = await vc.api.send<{ content: string }>('POST', `/api/schedules/${encodeURIComponent(name)}/run`)
    vc.notify(`${name} ran: ${res.content?.slice(0, 200) || '(no output)'}`)
  } catch (e) {
    vc.notify(`Run failed: ${(e as Error).message}`)
  }
}

// ---- triggers --------------------------------------------------------------

export async function createTrigger(vc: ViewCtx, body: Record<string, unknown>): Promise<boolean> {
  try {
    await vc.api.send('POST', '/api/triggers', body)
    vc.notify('Trigger created.')
    await vc.store.loadTriggers()
    return true
  } catch (e) {
    vc.notify('Create failed: ' + (e as Error).message)
    return false
  }
}

export async function updateTrigger(vc: ViewCtx, name: string, patch: Record<string, unknown>, note = 'Trigger updated.'): Promise<void> {
  try {
    await vc.api.send('PUT', `/api/triggers/${encodeURIComponent(name)}`, patch)
    vc.notify(note)
    await vc.store.loadTriggers()
  } catch (e) {
    vc.notify('Update failed: ' + (e as Error).message)
  }
}

export async function deleteTrigger(vc: ViewCtx, name: string): Promise<void> {
  try {
    await vc.api.send('DELETE', `/api/triggers/${encodeURIComponent(name)}`)
    await vc.store.loadTriggers()
  } catch (e) {
    vc.notify('Delete failed: ' + (e as Error).message)
  }
}

export async function runTrigger(vc: ViewCtx, name: string): Promise<void> {
  vc.notify(`Firing ${name}…`)
  try {
    const res = await vc.api.send<{ content: string }>('POST', `/api/triggers/${encodeURIComponent(name)}/run`)
    vc.notify(`${name} ran: ${res.content?.slice(0, 200) || '(no output)'}`)
  } catch (e) {
    vc.notify(`Run failed: ${(e as Error).message}`)
  }
}

// ---- inbox -----------------------------------------------------------------

export async function resolveInbox(vc: ViewCtx, id: string, decision: string): Promise<void> {
  try {
    await vc.api.send('POST', `/api/inbox/${encodeURIComponent(id)}/resolve`, { decision })
    await vc.store.loadInbox()
  } catch (e) {
    vc.notify('Resolve failed: ' + (e as Error).message)
  }
}

// ---- tool/toolset wiring (shared by settings + flow) -----------------------

// linkToolset idempotently adds a toolset to an agent's interactive tool policy,
// preserving the rest of the list.
export async function linkToolset(vc: ViewCtx, agentName: string, toolset: string): Promise<void> {
  const a = vc.store.agent(agentName)
  const cur = a?.spec?.tools?.interactive?.toolsets || []
  if (cur.includes(toolset)) return
  await vc.api.send('PUT', `/api/agents/${encodeURIComponent(agentName)}`, { interactiveToolsets: [...cur, toolset] })
  await vc.store.loadAgents()
}

// wireToolTo attaches a tool connection to the agent (its own grant) or to one
// of its toolsets, deriving families so the tool actually resolves.
export async function wireToolTo(vc: ViewCtx, agentName: string, target: string, cn: string): Promise<void> {
  if (target.startsWith('toolset:')) {
    const tsName = target.slice(8)
    const ts = vc.store.toolsets.find((x) => x.metadata.name === tsName)
    const conns = ts?.spec.connections || []
    if (!conns.includes(cn)) await updateToolset(vc, tsName, { connections: [...conns, cn], families: vc.store.familiesFor([...conns, cn]) })
    return
  }
  const a = vc.store.agent(agentName)
  const cur = a?.spec?.tools?.interactive?.connections || []
  if (cur.includes(cn)) return
  await vc.api.send('PUT', `/api/agents/${encodeURIComponent(agentName)}`, { interactiveConnections: [...cur, cn], interactiveFamilies: vc.store.familiesFor([...cur, cn]) })
  await vc.store.loadAgents()
}
