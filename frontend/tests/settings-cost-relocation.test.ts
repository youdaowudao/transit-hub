import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('../src/modules/admin/views/SettingsView.vue', import.meta.url), 'utf8')

describe('cost entry relocation', () => {
  it('removes account and manual cost entry from system settings', () => {
    expect(source).not.toContain('首页附加成本')
    expect(source).not.toContain('saveAdditionalCost')
    expect(source).not.toContain('saveRechargeFeeRate')
  })
})
