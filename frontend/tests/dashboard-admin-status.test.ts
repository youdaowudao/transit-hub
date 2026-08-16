import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const dashboardSource = readFileSync(
  new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url),
  'utf8',
)

describe('dashboard admin key status', () => {
  it('treats an admin key without expiry as unknown rather than expired', () => {
    const expiry = dashboardSource.match(/const adminExpiry = computed\([\s\S]*?\n\}\)\n/)?.[0] ?? ''
    expect(expiry).toContain("adminStatus.value.authMethod === 'admin_key'")
    expect(expiry).toContain('timeUnknown')
  })

  it('keeps data refresh failures on the data status path', () => {
    const failurePath = dashboardSource.match(/const loadAllData = async[\s\S]*?\n}\n\nconst adminExpiry/)?.[0] ?? ''
    expect(failurePath).toContain('refreshDataFailed.value = true')
    expect(failurePath).not.toContain('adminStatus.value = { authenticated: false }')
  })
})
