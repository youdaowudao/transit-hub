import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const viewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)
const localeSource = readFileSync(
  new URL('../src/locales/zh-CN.ts', import.meta.url),
  'utf8',
)

describe('connection health refresh prompt', () => {
  it('shows one total success message and hides successful site details', () => {
    expect(viewSource).toContain("refreshSummaryState === 'success'")
    expect(localeSource).toContain('本轮刷新全部成功')
    expect(viewSource).not.toContain('successfulRefreshSites')
  })

  it('shows only failed sites and keeps stale sites in the failure list', () => {
    expect(viewSource).toContain("filter(site => site.status !== 'success')")
    expect(viewSource).toContain('failedRefreshSites')
    expect(viewSource).toContain('retainedSnapshot')
    expect(localeSource).toContain('沿用旧数据')
  })

  it('shows a summary-level refresh failure phase when no site result is available', () => {
    expect(viewSource).toContain('refreshSummaryErrorKey')
    expect(viewSource).toContain('refreshSummaryErrorLabel')
    expect(viewSource).toContain('summaryFailureLabel')
    expect(viewSource).toContain("'admin.connectionHealth.errors.network'")
    expect(viewSource).toContain("'admin.connectionHealth.errors.request'")
    expect(viewSource).toContain("reason: 'network'")
    expect(viewSource).toContain("reason: 'request'")
    expect(localeSource).toContain('本轮刷新失败阶段')
  })

  it('uses a frontend-only elapsed timer while preserving the list and enabling other viewing actions', () => {
    expect(viewSource).toContain('refreshStartedAt')
    expect(viewSource).toContain('refreshWaitSeconds')
    expect(viewSource).toContain('refreshWaitTimer')
    expect(viewSource).toContain(':disabled="isLoading || refreshLoading"')
    expect(viewSource).toContain('isLoading && adminGroups.length === 0')
    expect(viewSource).not.toContain('fetch(')
  })

  it('keeps auxiliary failures separate from the unified refresh result', () => {
    expect(viewSource).toContain('auxiliaryFailures')
    expect(viewSource).toContain('runAuxiliaryRequest')
    expect(localeSource).toContain('辅助读取失败')
    expect(localeSource).toContain('Priority')
  })
})
