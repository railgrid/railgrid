// Assisted setup: the pure pieces of "let one of my agents stand up the
// self-hosted search backend for me".
//
// Doing it by hand means leaving for the Infrastructure provider to provision
// the searxng template and coming back here to create a websearch Connection
// naming it. The middle step is exactly what an agent can do: the hub's
// aggregate MCP endpoint is dialed for every interactive run, so
// infrastructure__* tools are on the table in chat with no tool grant at all.
//
// The prompt composer lives here (not inline in the view) so it is reviewable
// and testable on its own.

import type { Connection } from './types'

export type InstanceSize = 'small' | 'medium' | 'large'
export const INSTANCE_SIZES: InstanceSize[] = ['small', 'medium', 'large']

// The DNS-label rule every create form in this portal enforces (K8s object
// names): lowercase alphanumerics and dashes, not starting or ending with one.
export const DNS_LABEL_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/

export interface SearxngSetupInput {
  // connection is the websearch Connection this portal has just created; it
  // already names the instance the agent is about to provision.
  connection: string
  instance: string
  size: InstanceSize
}

// searxngSetupPrompt composes the single message the agent is handed. It is
// deliberately prescriptive: the agent must discover the template's real input
// schema instead of guessing it, and must not stop to ask questions — nobody is
// watching the chat to answer them.
//
// There is no credential anywhere in this prompt, and nothing for the user to
// copy back afterwards. The instance is never published; agents reach it over
// the infrastructure provider's data plane, addressed by the name the
// Connection already carries. See docs/platform-internal-networking.md.
export function searxngSetupPrompt({ connection, instance, size }: SearxngSetupInput): string {
  return [
    `Please provision a self-hosted SearXNG search backend for me using the infrastructure tools. Work through this end to end without asking me any questions — everything you need is below.`,
    ``,
    `1. Call \`describe_template\` for the \`searxng\` template first and use the exact input schema it returns. Do not guess input names.`,
    `2. Provision it with:`,
    `   - name: \`${instance}\``,
    `   - size: \`${size}\``,
    `3. This instance is internal-only: it is not published on any hostname and it has no access token. Do not create Secrets for it and do not look for a URL.`,
    `4. Then poll \`get_instance\` for \`${instance}\` until it reports ready. This usually takes a minute or two while the container image is pulled, so wait between checks instead of calling repeatedly back to back.`,
    `5. When it is ready, tell me so — the \`${connection}\` connection already points at it, so \`web_search\` starts working with nothing further from me.`,
    `6. Confirm it end to end by actually running a \`web_search\` and telling me what you got back.`,
  ].join('\n')
}

// selfHostedSearchConfigured reports whether this workspace already has a
// self-hosted search connection. The assisted card is an onboarding shortcut,
// so it retires itself once there is nothing left to onboard — and because the
// flow creates the Connection as its FIRST step, that happens the moment the
// user completes the dialog, not once the instance finishes provisioning.
export function selfHostedSearchConfigured(conns: Connection[]): boolean {
  return conns.some((c) => c.spec.type === 'websearch' && c.spec.config?.provider === 'searxng')
}

// Manual dismissal is remembered per WORKSPACE, not per browser: search set up
// in one workspace says nothing about another, and a card dismissed everywhere
// at once would hide the affordance from a workspace that still needs it.
const DISMISS_KEY = 'kedge:agents:assisted-search-dismissed'

function dismissKey(workspaceUUID: string | null): string {
  return `${DISMISS_KEY}:${workspaceUUID || 'none'}`
}

// Storage access is wrapped: private-browsing modes throw on both read and
// write, and a card that cannot remember a dismissal is far better than a
// portal that white-screens.
export function assistDismissed(workspaceUUID: string | null): boolean {
  try {
    return localStorage.getItem(dismissKey(workspaceUUID)) === '1'
  } catch {
    return false
  }
}

export function rememberAssistDismissed(workspaceUUID: string | null): void {
  try {
    localStorage.setItem(dismissKey(workspaceUUID), '1')
  } catch {
    // Ignored: dismissal degrades to per-session, which is survivable.
  }
}
