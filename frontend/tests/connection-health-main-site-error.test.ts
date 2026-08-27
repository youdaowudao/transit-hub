import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

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

  it('keeps the current-error and missing-reason copy available to the behavior test', () => {
    expect(localeSource).toContain('主站运行错误：{reason}')
    expect(localeSource).toContain('原因未提供')
  })
})
