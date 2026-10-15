// A presentation preference, never an authorization decision. The in-memory
// fallback prevents Workspace switches from reopening onboarding when storage
// is unavailable. localStorage is already scoped to the current hub origin.
const shown = new Set<string>()
const prefix = 'railgrid:portal:welcome-seen:'

export function claimFirstWelcome(user: string): boolean {
  if (!user || shown.has(user)) return false
  shown.add(user)
  try {
    const key = `${prefix}${encodeURIComponent(user)}`
    if (localStorage.getItem(key) === 'true') return false
    // Legacy dismissal lists have no account owner. Do not assign another
    // person's history to this user on a shared browser.
    localStorage.setItem(key, 'true')
  } catch { /* The session marker still prevents repeat prompts. */ }
  return true
}

// The login record is assigned atomically by the auth store. Wait for it rather
// than claiming a provisional self identity that changes when login hydrates.
export function welcomeIdentity(auth: {
  user?: { userId?: string; email?: string } | null
  self?: { rbacIdentity?: string } | null
}): string {
  if (!auth.user) return ''
  return auth.user.userId || auth.user.email || auth.self?.rbacIdentity || ''
}
