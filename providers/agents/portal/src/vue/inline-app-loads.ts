interface Flight<T> {
  key: string
  scopeKey: string
  isCurrent: () => boolean
  controller: AbortController
  task: (signal: AbortSignal) => Promise<T | null>
  promise: Promise<T | null>
  resolve: (value: T | null) => void
  reject: (reason?: unknown) => void
  started: boolean
  settled: boolean
}

/** Per-chat in-flight dedupe and a small bound on attended inline reads. */
export class InlineAppLoadCoordinator<T> {
  private readonly flights = new Map<string, Flight<T>>()
  private readonly queue: Flight<T>[] = []
  private active = 0

  constructor(private readonly concurrency = 2) {}

  load(key: string, scopeKey: string, isCurrent: () => boolean, task: (signal: AbortSignal) => Promise<T | null>): Promise<T | null> {
    const existing = this.flights.get(key)
    if (existing && existing.scopeKey === scopeKey && !existing.controller.signal.aborted && existing.isCurrent()) {
      return existing.promise
    }
    if (existing) this.cancel(existing)

    let resolve!: (value: T | null) => void
    let reject!: (reason?: unknown) => void
    const promise = new Promise<T | null>((resolvePromise, rejectPromise) => {
      resolve = resolvePromise
      reject = rejectPromise
    })
    const flight: Flight<T> = {
      key, scopeKey, isCurrent, task, promise, resolve, reject,
      controller: new AbortController(), started: false, settled: false,
    }
    this.flights.set(key, flight)
    this.queue.push(flight)
    this.pump()
    return promise
  }

  /** Abort stale work when chat authority, session, or inspector mode changes. */
  abortAll(): void {
    for (const flight of [...this.flights.values()]) this.cancel(flight)
    this.pump()
  }

  private cancel(flight: Flight<T>): void {
    flight.controller.abort()
    if (this.flights.get(flight.key) === flight) this.flights.delete(flight.key)
    if (!flight.started) {
      const index = this.queue.indexOf(flight)
      if (index !== -1) this.queue.splice(index, 1)
      this.settle(flight, null)
    }
  }

  private settle(flight: Flight<T>, value: T | null): void {
    if (flight.settled) return
    flight.settled = true
    flight.resolve(value)
  }

  private pump(): void {
    while (this.active < this.concurrency && this.queue.length) {
      const flight = this.queue.shift()!
      if (flight.settled) continue
      if (flight.controller.signal.aborted || !flight.isCurrent()) {
        this.settle(flight, null)
        if (this.flights.get(flight.key) === flight) this.flights.delete(flight.key)
        continue
      }
      flight.started = true
      this.active += 1
      void Promise.resolve().then(() => flight.task(flight.controller.signal)).then(
        value => this.settle(flight, flight.isCurrent() && !flight.controller.signal.aborted ? value : null),
        error => {
          if (flight.controller.signal.aborted || !flight.isCurrent()) this.settle(flight, null)
          else { flight.settled = true; flight.reject(error) }
        },
      ).finally(() => {
        this.active -= 1
        if (this.flights.get(flight.key) === flight) this.flights.delete(flight.key)
        this.pump()
      })
    }
  }
}
