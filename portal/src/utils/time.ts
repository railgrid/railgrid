// Shared timestamp formatters for ISO 8601 strings coming from the k8s API.

// formatAge returns a short relative duration like "5d", "2h", "30m", "10s".
// Empty input returns an empty string. Invalid input returns the original.
export function formatAge(iso: string | null | undefined): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return iso
  const diff = Math.max(0, Date.now() - t)
  const s = Math.floor(diff / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  const d = Math.floor(h / 24)
  return `${d}d`
}

// formatDateTime renders an ISO timestamp as a locale-aware, human-readable
// absolute string (e.g. "2026-05-07 14:34:45"). Empty input returns an em-dash.
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}

// formatDateTimeWithAge combines absolute and relative forms:
// "2026-05-07 14:34:45 (5d ago)". Useful for tooltips or detail rows.
export function formatDateTimeWithAge(iso: string | null | undefined): string {
  if (!iso) return '—'
  const dt = formatDateTime(iso)
  const age = formatAge(iso)
  return age ? `${dt} (${age} ago)` : dt
}
