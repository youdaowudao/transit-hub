import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const detailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)
const typeSource = readFileSync(
  new URL('../src/modules/admin/types/connectionHealth.ts', import.meta.url),
  'utf8',
)
const localeSource = readFileSync(
  new URL('../src/locales/zh-CN.ts', import.meta.url),
  'utf8',
)

describe('connection health main-site account errors', () => {
  it('keeps the main-site error as an optional account field', () => {
    expect(typeSource).toContain('mainSiteError?: string')
  })

  it('renders a red Sub2API runtime error without requiring account status to be error', () => {
    expect(detailSource).toContain("account.status?.toLowerCase() === 'error'")
    expect(detailSource).toContain('Boolean(account.mainSiteError?.trim())')
    expect(detailSource).toContain('|| Boolean(account.mainSiteError?.trim())')
    expect(detailSource).toContain('account.mainSiteError')
    expect(detailSource).toContain('text-destructive')
    expect(detailSource).toContain('upstreamAccountActive')
    expect(detailSource).toContain('schedulableLabel(account)')
    expect(detailSource).toContain('account.modelHealth')
  })

  it('has a safe fallback reason when the main site does not provide one', () => {
    expect(detailSource).toContain("account.status?.toLowerCase() === 'error'")
    expect(detailSource).toContain('mainSiteErrorReasonUnavailable')
    expect(localeSource).toContain('主站运行错误：{reason}')
    expect(localeSource).toContain('原因未提供')
  })
})
