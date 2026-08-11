import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

const componentSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)
const viewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)

describe('connection health scheduling button state', () => {
  it('uses a persistent green button for schedulable accounts', () => {
    expect(componentSource).toContain(
      "? 'bg-emerald-600 text-white hover:bg-emerald-700 hover:text-white dark:bg-emerald-500 dark:text-white dark:hover:bg-emerald-400 dark:hover:text-white'",
    )
  })

  it('keeps unschedulable accounts in the muted button state', () => {
    expect(componentSource).toContain(
      ": 'text-muted-foreground hover:bg-surface hover:text-foreground'",
    )
  })

  it('requires confirmation before changing Sub2API scheduling', () => {
    const confirmIndex = viewSource.indexOf('if (!confirm(`确认对 ${account.targetId} 执行「${action}」？`)) return')
    const updateIndex = viewSource.indexOf('await updateTargetSchedulable(account.targetId, !account.schedulable)')

    expect(confirmIndex).toBeGreaterThan(-1)
    expect(updateIndex).toBeGreaterThan(confirmIndex)
  })
})
