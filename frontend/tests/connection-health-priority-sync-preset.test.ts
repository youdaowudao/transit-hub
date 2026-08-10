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
const connectionHealthComposableSource = readFileSync(
  new URL('../src/modules/admin/composables/useConnectionHealth.ts', import.meta.url),
  'utf8',
)
const connectionHealthApiSource = readFileSync(
  new URL('../src/modules/admin/api/connectionHealth.ts', import.meta.url),
  'utf8',
)
const zhCNSource = readFileSync(new URL('../src/locales/zh-CN.ts', import.meta.url), 'utf8')

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

  it('loads the lightweight priority state independently and preserves it during local overview aggregation', () => {
    expect(connectionHealthApiSource).toContain("'/connection-health/priority-sync'")
    expect(connectionHealthComposableSource).toContain('const prioritySync = ref<PriorityWorkspaceSyncState | null>(null)')
    expect(connectionHealthComposableSource).toContain('const prioritySyncErrorKey = ref(\'\')')
    expect(connectionHealthComposableSource).toContain('loadPrioritySync()')
    expect(connectionHealthComposableSource).toContain('loadAdminGroups(opts), loadGroups({ silent: true }), loadPrioritySync()')
    expect(connectionHealthViewSource).toContain('prioritySync.writebackSpreadSeconds')
    expect(connectionHealthViewSource).toContain('prioritySync.pendingTargetCount')
    expect(connectionHealthViewSource).toContain('prioritySync.lastWriteRoundTargetCount')
    expect(connectionHealthViewSource).toContain('prioritySync && prioritySync.lastWriteRoundTargetCount > 0')
    expect(connectionHealthViewSource).not.toContain('overview.prioritySync')
  })

  it('keeps the no-history and account wording in the existing priority area', () => {
    expect(connectionHealthViewSource).toContain("prioritySync.noHistory")
    expect(connectionHealthViewSource).toContain("prioritySync.lastWriteRoundTargetValue")
    expect(connectionHealthViewSource).toContain("prioritySync.pendingTargetsValue")
  })

  it('describes the retained interval as failure retry protection', () => {
    expect(connectionHealthViewSource).toContain("prioritySync.intervalPairValue")
    expect(policyDrawerSource).toContain("priorityWriteIntervalLabel")
    expect(zhCNSource).toContain("interval: '失败重试/校验间隔'")
    expect(zhCNSource).toContain("priorityWriteIntervalLabel: 'Priority 失败重试间隔'")
    expect(zhCNSource).toContain("min_write_interval: '写入失败，等待重试间隔'")
    expect(zhCNSource).not.toContain('最短 Priority 写回间隔')
  })
})
