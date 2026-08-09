import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { preservePriorityMaxPendingAge } from '../src/modules/admin/utils/connectionHealthPolicy'

const policyDrawerSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/PolicyConfigDrawer.vue', import.meta.url),
  'utf8',
)
const connectionHealthViewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)

describe('connection health priority sync preset', () => {
  it('preserves a hidden non-default max pending age during ordinary edits', () => {
    expect(preservePriorityMaxPendingAge(900, 30)).toBe(900)
  })

  it('keeps max pending age valid when the write interval is raised', () => {
    expect(preservePriorityMaxPendingAge(300, 600)).toBe(600)
  })

  it('uses the B-phase default for legacy policies without the field', () => {
    expect(preservePriorityMaxPendingAge(undefined, 30)).toBe(300)
  })

  it('keeps the E spread default, bounds, and save payload together', () => {
    expect(policyDrawerSource).toContain('priorityWritebackSpreadSeconds: 1')
    expect(policyDrawerSource).toContain('priorityWritebackSpreadSeconds.value < 1')
    expect(policyDrawerSource).toContain('priorityWritebackSpreadSeconds.value > 10')
    expect(policyDrawerSource).toContain('writebackSpreadSeconds: priorityWritebackSpreadSeconds.value')
    expect(policyDrawerSource).toContain('v-model.number="priorityWritebackSpreadSeconds"')
    expect(policyDrawerSource).toContain('min="1" max="10"')
  })

  it('shows the E spread and pending target count in the existing overview', () => {
    expect(connectionHealthViewSource).toContain('overview.prioritySync.writebackSpreadSeconds')
    expect(connectionHealthViewSource).toContain('overview.prioritySync.pendingTargetCount')
  })
})
