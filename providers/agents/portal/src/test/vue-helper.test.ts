import { defineComponent, onBeforeUnmount } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { mountVue, unmountVueApps } from './vue-helper'

describe('Vue test cleanup', () => {
  it('unmounts every registered app so component cleanup runs', async () => {
    const cleanup = vi.fn()
    const component = defineComponent({
      setup() {
        onBeforeUnmount(cleanup)
        return () => 'mounted'
      },
    })

    await mountVue(component, {})
    await mountVue(component, {})
    unmountVueApps()

    expect(cleanup).toHaveBeenCalledTimes(2)
  })
})
