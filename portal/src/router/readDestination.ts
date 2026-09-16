// Retry temporary transport/service failures without publishing an error while
// startup is still recovering. Authorization and not-found answers are final.
const retryableStatuses = new Set([408, 429, 500, 502, 503, 504])
const retryDelays = [250, 750]

export async function readDestination(
  read: () => Promise<Response>,
  current: () => boolean,
): Promise<Response | null> {
  for (let attempt = 0; current(); attempt++) {
    try {
      const response = await read()
      if (!current()) return null
      if (!retryableStatuses.has(response.status) || attempt === retryDelays.length) return response
    } catch (error) {
      // Fetch uses TypeError for network failures. Do not retry session-change
      // exceptions or other application errors.
      if (!current()) return null
      if (!(error instanceof TypeError) || attempt === retryDelays.length) throw error
    }
    await new Promise((resolve) => setTimeout(resolve, retryDelays[attempt]))
  }
  return null
}
