// Shared types for the agents micro-frontend. Entity interfaces mirror the
// backend REST projections (providers/agents/api); only the fields the UI reads
// are declared. KedgeContext is the host↔element contract (set as a JS property
// by the portal's ProviderFrame).

export interface KedgeContext {
  token?: string | null
  user?: { email?: string; sub?: string } | null
  tenant?: string | null
  theme?: 'light' | 'dark' | 'system'
  basePath?: string
}

export interface Agent {
  metadata: { name: string }
  spec: {
    displayName?: string
    description?: string
    systemPrompt?: string
    autonomy?: string
    models?: Record<string, string>
    modelFallbacks?: string[]
    defaultNotifyConnection?: string
    delegates?: string[]
    budget?: { window?: string; usdLimit?: string; tokenLimit?: number }
    tools?: {
      interactive?: { families?: string[]; toolsets?: string[]; connections?: string[] }
      background?: { families?: string[]; toolsets?: string[]; connections?: string[] }
    }
  }
}

export interface Credential {
  name: string
  provider?: string
  baseURL?: string
  model?: string
  hasAPIKey?: boolean
}

export interface Schedule {
  metadata: { name: string }
  spec: { agentRef: string; type: string; schedule?: string; runAt?: string; timeZone?: string; task?: string; checklist?: string; suspend?: boolean }
  status?: { nextRun?: string; lastRun?: string; disabledReason?: string }
}

export interface Connection {
  metadata: { name: string }
  spec: { type: string; displayName?: string; baseURL?: string; channel?: string; auth?: string }
  status?: { phase?: string; webhookPath?: string; oauthConnected?: boolean }
}

export interface Trigger {
  metadata: { name: string }
  spec: { agentRef: string; source: string; connectionRef?: string; task?: string; suspend?: boolean }
  status?: { lastFired?: string; webhookPath?: string }
}

export interface Toolset {
  metadata: { name: string }
  spec: { displayName?: string; description?: string; families?: string[]; connections?: string[]; requireApproval?: string[] }
  status?: { usedBy?: number }
}

export interface InboxItem {
  id: string
  agentName: string
  kind: string
  state: string
  prompt: string
  createdAt: string
}

export interface ChatMessage {
  role: 'user' | 'assistant' | 'tool'
  content: string
  error?: boolean
}

// SessionMeta mirrors the backend store.Session summary for the session picker.
export interface SessionMeta {
  id: string
  preview?: string
  messageCount: number
  createdAt: string
  lastActivity: string
}

export const sessionLabel = (s: SessionMeta): string => (s.preview && s.preview.trim()) || 'New chat'

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string)
}

// fmtTime renders an ISO timestamp as a compact relative string ("in 5m",
// "2h ago"), falling back to a locale date for anything beyond ~2 days.
export function fmtTime(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const diff = d.getTime() - Date.now()
  const abs = Math.abs(diff)
  const m = Math.round(abs / 60000)
  if (m < 60) return diff > 0 ? `in ${m}m` : `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 48) return diff > 0 ? `in ${h}h` : `${h}h ago`
  return d.toLocaleDateString()
}
