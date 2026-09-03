// Tiny host entry: register the provider elements synchronously, then let each
// element load only its own implementation when connected. In particular, the
// dashboard tile no longer downloads and parses the full App Studio workspace.
// Dynamic import() is valid in classic scripts, so the existing host loader and
// custom-element contract remain unchanged.
import { registerAppStudioElements } from './element'

const bootstrapGeneration = typeof document === 'undefined'
  ? undefined
  : (document.currentScript as HTMLScriptElement | null)?.dataset.farosProviderBootstrapGeneration

registerAppStudioElements(bootstrapGeneration)
