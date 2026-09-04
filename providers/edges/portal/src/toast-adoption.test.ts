import { describe, expect, it } from 'vitest'

import app from './App.vue?raw'
import detail from './Detail.vue?raw'
import serviceCreate from './ServiceCreate.vue?raw'
import serviceEdit from './ServiceEdit.vue?raw'
import services from './Services.vue?raw'
import workloads from './Workloads.vue?raw'

const sources = [app, detail, serviceCreate, serviceEdit, services, workloads]

describe('Edges toast adoption', () => {
  it('covers every silent-success mutation entry point', () => {
    expect(sources.join('\n').match(/\btoast\(/g)).toHaveLength(10)
    expect(app).toContain("toast('info', `${edge.type === 'server' ? 'Server' : 'Cluster'} deletion requested for ${edge.name}.`)")
    expect(detail).toContain("toast('ok', `Service credentials saved for ${name}.`)")
    expect(serviceCreate).toContain("toast('info', `Service creation requested for ${name}.`)")
    expect(serviceEdit).toContain("toast('ok', `Service configuration saved for ${name}.`)")
    expect(serviceEdit).toContain("toast('ok', `Service credentials saved for ${name}.`)")
    expect(sources.join('\n').match(/Service deletion requested/g)).toHaveLength(3)
    expect(workloads).toContain("toast('info', `Workload deletion requested for ${w.name}.`)")
  })

  it('keeps contextual failures inline instead of producing error toasts', () => {
    expect(sources.join('\n')).not.toContain("toast('error'")
  })
})
