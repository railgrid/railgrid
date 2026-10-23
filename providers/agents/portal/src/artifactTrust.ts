import { parseSavedCaptureCitation } from './agentArtifacts'
import type { ChatMessage, SavedCaptureCitation } from './types'

export interface SavedCaptureReceipt {
  runID: string
  toolCallID: string
}

export function sameSavedCapture(left: SavedCaptureCitation, right: SavedCaptureCitation): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

/** Find only a successful, visible attended citation in the active transcript. */
export function findSavedCaptureReceipt(messages: readonly ChatMessage[], citation: SavedCaptureCitation): SavedCaptureReceipt | null {
  for (const message of messages) {
    if (message.role !== 'assistant' || !message.runID) continue
    for (const tool of message.tools) {
      if (tool.name !== 'open_analysis_artifact' || tool.pending || tool.error || typeof tool.result !== 'string') continue
      const savedCapture = parseSavedCaptureCitation(tool.savedCapture)
      if (savedCapture && sameSavedCapture(savedCapture, citation)) return { runID: message.runID, toolCallID: tool.toolCallID || tool.id }
    }
  }
  return null
}

export function savedCaptureReceiptIsCurrent(
  messages: readonly ChatMessage[],
  activeSessionID: string,
  detail: { runID: string; sessionID: string; toolCallID: string; savedCapture: SavedCaptureCitation },
): boolean {
  if (!activeSessionID || detail.sessionID !== activeSessionID) return false
  const message = messages.find(item => item.role === 'assistant' && item.runID === detail.runID)
  const tool = message?.tools.find(item => (item.toolCallID || item.id) === detail.toolCallID)
  if (!tool || tool.name !== 'open_analysis_artifact' || tool.pending || tool.error || typeof tool.result !== 'string') return false
  const savedCapture = parseSavedCaptureCitation(tool.savedCapture)
  return !!savedCapture && sameSavedCapture(savedCapture, detail.savedCapture)
}
