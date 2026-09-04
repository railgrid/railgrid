import {
  inject,
  onBeforeUnmount,
  onMounted,
  provide,
  ref,
  watch,
  type InjectionKey,
  type Ref,
  type ShallowRef,
} from 'vue'
import type { ApiClient } from '../api'
import { hashFor, type Route } from '../router'
import type { AppStore } from '../store'

export interface AgentsRuntime {
  store: ShallowRef<AppStore>
  api: ShallowRef<ApiClient>
  route: Ref<Route>
  authorityEpoch: Ref<number>
  host: Ref<HTMLElement | null>
}

const AGENTS_RUNTIME: InjectionKey<AgentsRuntime> = Symbol('agents-runtime')

export function provideAgentsRuntime(runtime: AgentsRuntime): void {
  provide(AGENTS_RUNTIME, runtime)
}

export function useAgentsRuntime(): AgentsRuntime | null {
  return inject(AGENTS_RUNTIME, null)
}

// AppStore intentionally remains an EventTarget rather than becoming a Vue
// proxy. Its request/adoption generations depend on stable object identity.
// Reading this revision from a computed/render function makes every store
// `change` event invalidate that view without replacing component-local drafts.
export function useStoreRevision(getStore: () => AppStore): Ref<number> {
  const revision = ref(0)
  let bound: AppStore | null = null
  const onChange = () => { revision.value += 1 }
  const bind = (store: AppStore) => {
    if (bound === store) return
    bound?.removeEventListener('change', onChange)
    bound = store
    bound.addEventListener('change', onChange)
    revision.value += 1
  }

  watch(getStore, bind, { immediate: true, flush: 'sync' })
  onBeforeUnmount(() => {
    bound?.removeEventListener('change', onChange)
    bound = null
  })
  return revision
}

export interface AuthoritySnapshot {
  readonly store: AppStore
  readonly api: ApiClient
  readonly epoch: number
  readonly routeKey: string
  readonly host: HTMLElement | null
}

// Confirmations and multi-step writes may resolve after their opening surface
// has disappeared. This guard preserves the former StoreElement authority
// contract across Vue route remounts and synchronous context rotations.
export function useAuthorityGuard(
  getStore: () => AppStore,
  getApi: () => ApiClient,
): {
  captureAuthority: () => AuthoritySnapshot
  authorityIsCurrent: (snapshot: AuthoritySnapshot) => boolean
} {
  const runtime = useAgentsRuntime()
  let mounted = false
  onMounted(() => { mounted = true })
  onBeforeUnmount(() => { mounted = false })

  const captureAuthority = (): AuthoritySnapshot => ({
    store: getStore(),
    api: getApi(),
    epoch: runtime?.authorityEpoch.value ?? 0,
    routeKey: runtime ? hashFor(runtime.route.value) : location.hash,
    host: runtime?.host.value ?? null,
  })

  const authorityIsCurrent = (snapshot: AuthoritySnapshot): boolean => {
    if (!mounted || getStore() !== snapshot.store || getApi() !== snapshot.api) return false
    if (!runtime) return location.hash === snapshot.routeKey
    return (
      runtime.store.value === snapshot.store &&
      runtime.api.value === snapshot.api &&
      runtime.authorityEpoch.value === snapshot.epoch &&
      hashFor(runtime.route.value) === snapshot.routeKey &&
      runtime.host.value === snapshot.host &&
      Boolean(snapshot.host?.isConnected)
    )
  }

  return { captureAuthority, authorityIsCurrent }
}
