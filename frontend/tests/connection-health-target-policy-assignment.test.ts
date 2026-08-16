import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const detailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)
const viewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)
const dialogSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/TargetPolicyAssignmentDialog.vue', import.meta.url),
  'utf8',
)

describe('connection health account policy assignment', () => {
  it('exposes an account-level assignment action from the group detail table', () => {
    expect(detailSource).toContain("(event: 'assign-policy', account: AdminGroupAccount): void")
    expect(detailSource).toContain("emit('assign-policy', account)")
  })

  it('opens the existing target policy dialog and refreshes effective health after saving', () => {
    expect(viewSource).toContain("import TargetPolicyAssignmentDialog from '../components/dashboard/TargetPolicyAssignmentDialog.vue'")
    expect(viewSource).toContain('@assign-policy="onAssignPolicy"')
    expect(viewSource).toContain(':target-id="policyAssignmentTarget?.targetId ?? \'\'"')
    expect(viewSource).toContain('await Promise.all([loadAdminGroups({ silent: true }), loadPrioritySyncStatus()])')
  })

  it('ignores stale target-policy reads after the dialog target changes', () => {
    expect(dialogSource).toContain('let loadSequence = 0')
    expect(dialogSource).toContain('if (requestSequence !== loadSequence) return')
  })
})
