import type { InjectionKey, Ref } from 'vue'

/**
 * Shared authority generation for route-owned resource mutations.
 *
 * App.vue advances the provided ref synchronously when the host context
 * changes. Forms compare the value captured at submit/read start, so a
 * resolved continuation is rejected even while Vue is still waiting to flush
 * the keyed component unmount.
 */
export const contextGenerationKey: InjectionKey<Readonly<Ref<number>>> = Symbol('databricks-context-generation')
