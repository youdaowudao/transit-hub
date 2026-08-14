import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const detailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)
const localeSource = readFileSync(new URL('../src/locales/zh-CN.ts', import.meta.url), 'utf8')

describe('connection health multiplier resolution display', () => {
  it('shows the exact unresolved status and current health-band action', () => {
    for (const status of ['unassociated', 'missing', 'conflict', 'unavailable']) {
      expect(detailSource).toContain(`case '${status}'`)
    }
    expect(localeSource).toContain('未关联真实连接，已按当前健康档末位排序')
    expect(localeSource).toContain('Key 分组或倍率缺失，已按当前健康档末位排序')
    expect(localeSource).toContain('多 Key 或分组冲突，已按当前健康档末位排序')
    expect(localeSource).toContain('本轮上游查询暂不可用，已按当前健康档末位排序')
  })

  it('does not claim that unavailable multipliers hold the old priority', () => {
    expect(localeSource).not.toContain('保持上次倍率和 priority')
    expect(localeSource).not.toContain('上游倍率与本地回退都不可用')
  })
})
