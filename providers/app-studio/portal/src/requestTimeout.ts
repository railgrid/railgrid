export class RequestTimeoutError extends Error {
  constructor() {
    super('request timed out')
    this.name = 'RequestTimeoutError'
  }
}

/** Run an async transport with an AbortSignal that fires at the deadline. */
export async function withRequestTimeout<T>(
  timeoutMS: number | undefined,
  run: (signal?: AbortSignal) => Promise<T>,
): Promise<T> {
  if (!timeoutMS || timeoutMS <= 0) return run()

  const controller = new AbortController()
  let timedOut = false
  const timeout = setTimeout(() => {
    timedOut = true
    controller.abort()
  }, timeoutMS)
  try {
    return await run(controller.signal)
  } catch (error) {
    if (timedOut) throw new RequestTimeoutError()
    throw error
  } finally {
    clearTimeout(timeout)
  }
}
