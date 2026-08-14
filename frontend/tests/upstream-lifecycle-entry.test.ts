import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const upstreamSource = readFileSync(
  new URL('../src/modules/admin/views/UpstreamView.vue', import.meta.url),
  'utf8',
)

const groupsModalSource = upstreamSource.slice(
  upstreamSource.indexOf('<!-- Groups Modal -->'),
  upstreamSource.indexOf('<!-- Add Site Modal -->'),
)
const editModalSource = upstreamSource.slice(
  upstreamSource.indexOf('<!-- Add Site Modal -->'),
  upstreamSource.indexOf('<SiteSettingsModal'),
)

describe('upstream site lifecycle entry', () => {
  it('keeps the lifecycle switch out of the available-groups modal', () => {
    expect(groupsModalSource).not.toContain("admin.upstream.lifecycle.label")
    expect(groupsModalSource).not.toContain('@click="toggleSiteEnabled')
  })

  it('shows an independent lifecycle switch while editing a site', () => {
    expect(editModalSource).toContain('v-if="editingSite"')
    expect(editModalSource).toContain('type="button"')
    expect(editModalSource).toContain('role="switch"')
    expect(editModalSource).toContain('@click="toggleSiteEnabled(editingSite)"')
    expect(editModalSource).toContain('enabledErrorKeys.get(editingSite.id)')
  })
})
