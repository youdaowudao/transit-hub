import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { resolvePrioritySyncFailureMessage } from '../src/modules/admin/utils/connectionHealthMultiplier'

const healthViewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)
const setupDrawerSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue', import.meta.url),
  'utf8',
)
const routerSource = readFileSync(new URL('../src/router.ts', import.meta.url), 'utf8')

describe('connection health asynchronous Priority synchronization', () => {
  it('keeps the saved group update local instead of reloading the full health page', () => {
    const handler = healthViewSource.match(/const onSetupSaved = async[\s\S]*?\n}\n\n\/\/ 手动探活弹窗/)?.[0] ?? ''
    expect(handler).toContain('applySavedGroupConfiguration(configuration)')
    expect(handler).toContain("runAuxiliaryRequest('priority', loadPrioritySyncStatus)")
    expect(handler).toContain("runAuxiliaryRequest('policies'")
    expect(handler).not.toContain('loadAll(')
  })

  it('shows persisted asynchronous failure after entering the page again while local save failures keep the drawer open', () => {
    expect(healthViewSource).toContain("void runAuxiliaryRequest('priority', loadPrioritySyncStatus)")
    expect(healthViewSource).toContain("prioritySyncStatus?.status === 'failed'")
    expect(healthViewSource).toContain("prioritySyncStatus?.status === 'pending' || prioritySyncStatus?.status === 'running'")

    const saveFailure = setupDrawerSource.match(/if \('errorKey' in outcome\) \{[\s\S]*?\n  }\n  clearPendingLegacyPolicy/)?.[0] ?? ''
    expect(saveFailure).toContain('errorKey.value = outcome.errorKey')
    expect(saveFailure).toContain("phase.value = 'ready'")
    expect(saveFailure).toContain('return')
  })

  it('redirects only the explicit no-current-workspace error', () => {
    expect(routerSource).toContain("error.message === 'admin.adminAccounts.errors.noCurrentAccount'")
    expect(routerSource).toContain('setWorkspaceChecked(true, false)')
    expect(routerSource).toContain('setRouteError(error)')
    expect(routerSource).toContain('return false')
  })

  it('shows an exact blocker count only for multiplier metadata failures', () => {
    const message = resolvePrioritySyncFailureMessage({
      workspaceId: 'ws1',
      status: 'failed',
      errorKey: 'admin.connectionHealth.errors.priorityMetadataUnavailable',
      failedCount: 3,
    }, '2026/8/20 22:01:05', '倍率资料不完整')

    expect(message.key).toBe('admin.connectionHealth.prioritySync.failed')
    expect(message.params).toMatchObject({ workspace: 'ws1', count: 3 })
  })

  it('does not present inventory failure estimates as an exact account count', () => {
    const message = resolvePrioritySyncFailureMessage({
      workspaceId: 'ws1',
      status: 'failed',
      errorKey: 'admin.connectionHealth.errors.priorityInventoryIncomplete',
      failedCount: 1,
    }, '2026/8/20 22:01:05', '主站分组库存不完整')

    expect(message.key).toBe('admin.connectionHealth.prioritySync.failedWithoutCount')
    expect(message.params).toEqual({
      workspace: 'ws1',
      time: '2026/8/20 22:01:05',
      reason: '主站分组库存不完整',
    })
  })
})
