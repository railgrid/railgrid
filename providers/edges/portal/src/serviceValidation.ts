export function servicePortError(value: unknown): string | null {
  const port = Number(value)
  return Number.isInteger(port) && port >= 1 && port <= 65535
    ? null
    : 'Enter a whole-number port from 1 to 65535.'
}
