import { ref } from 'vue'

// One submission owns readiness and creation together. Cancellation or a scope
// change invalidates that ownership before an old readiness response can create.
export function useProjectCreationSubmit() {
  const pending = ref(false)
  let generation = 0
  function invalidate() { generation++; pending.value = false }
  async function run(ready: () => Promise<boolean>, create: () => Promise<void>) {
    if (pending.value) return
    const current = ++generation
    pending.value = true
    try {
      if (await ready() && current === generation) await create()
    } finally {
      if (current === generation) pending.value = false
    }
  }
  return { pending, run, invalidate }
}
