import { describe, expect, it } from 'vitest'

import detail from './views/InstanceDetailPage.vue?raw'
import list from './views/InstanceListPage.vue?raw'
import provision from './views/ProvisionPage.vue?raw'

describe('Infrastructure toast adoption', () => {
  it('covers provisioning and both deletion entry points', () => {
    expect(provision).toContain("toast('info', `Provisioning started for ${inst.name}.`)")
    expect(detail).toContain("toast('info', `Instance deletion requested for ${deletingInstance.name}.`)")
    expect(list).toContain("toast('info', `Instance deletion requested for ${currentInstance.name}.`)")
    expect([provision, detail, list].join('\n').match(/\btoast\(/g)).toHaveLength(3)
  })

  it('does not duplicate contextual failures with error toasts', () => {
    expect([provision, detail, list].join('\n')).not.toContain("toast('error'")
  })
})
