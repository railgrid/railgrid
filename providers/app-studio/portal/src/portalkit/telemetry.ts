// Product telemetry from a provider UI. On by default, and the operator can
// turn it off; docs/telemetry.md is the contract for what may be reported.
//
// A CLICK needs no code: put data-track="create_edge" (and optional
// data-track-<label>="...") on the control, and the host's listener records it.
//
// providerTrack is for what is not a click: an outcome ("edge_created",
// "edge_create_failed") or a step in a flow. The host adds the provider's
// name; an organization's own provider gets a function that records nothing.
//
// Names are lower_snake_case. Labels are FIXED strings chosen by an engineer
// ("limit", "kubernetes"). Never pass an error message, a resource name or
// anything a user typed.

export type ProviderTrack = (name: string, props?: Record<string, string>) => void

// ProviderTrackContext is the slice of railgridContext providerTrack reads.
export interface ProviderTrackContext {
  track?: ProviderTrack | null
}

// providerTrack returns the function to report with. It never throws and does
// nothing on a host that predates telemetry.
export function providerTrack(ctx: ProviderTrackContext | null | undefined): ProviderTrack {
  return (name, props) => {
    try {
      ctx?.track?.(name, props)
    } catch {
      // Telemetry never affects the provider.
    }
  }
}
