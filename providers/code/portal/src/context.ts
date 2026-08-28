import type { InjectionKey, Ref } from 'vue'

/**
 * Shared authority generation for route-owned Code mutations.
 *
 * App.vue advances the provided ref synchronously whenever the shell changes
 * the workspace, caller, token, or provider base path. Create forms capture
 * the value before starting an async operation and compare it at every
 * continuation, so a stale result is rejected before Vue flushes the keyed
 * route unmount.
 */
export const contextGenerationKey: InjectionKey<Readonly<Ref<number>>> = Symbol('code-context-generation')
