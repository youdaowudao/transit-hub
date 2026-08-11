import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const upstreamSource = readFileSync(
  new URL('../src/modules/admin/views/UpstreamView.vue', import.meta.url),
  'utf8',
)
const healthSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)

describe('connection health group focus navigation', () => {
  it('passes the connected group identity from the upstream page', () => {
    expect(upstreamSource).toContain('focusGroupId: ownGroupId || undefined')
    expect(upstreamSource).toContain('focusGroupName: ownGroupId ? undefined : ownGroupName || undefined')
  })

  it('selects the requested health group and consumes the query', () => {
    expect(healthSource).toContain('const focusId = routeQueryValue(route.query.focusGroupId).trim()')
    expect(healthSource).toContain('const focusName = routeQueryValue(route.query.focusGroupName).trim()')
    expect(healthSource).toContain('selectedGroupId.value = target.id')
    expect(healthSource).toContain('await clearGroupFocusQuery()')
  })
})
