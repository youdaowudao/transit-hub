import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { listMassEmailUsers } from '@/modules/admin/api/massEmail'
import type { MassEmailUser } from '@/modules/admin/types/massEmail'
import {
  dateKeyInTimeZone,
  defaultSelectedDates,
  formatLastUsedAt,
  groupUsersByLastUsedDate,
  normalizeSelectedDates,
  pageContainsDateBefore,
  pageIsLastUsedTimeTail,
} from '@/modules/admin/utils/userLastUsed'

const viewSource = readFileSync(
  fileURLToPath(new URL('../src/modules/admin/views/UserLastUsedView.vue', import.meta.url)),
  'utf8',
)

const user = (id: string, username: string | null, lastUsedAt: string | null): MassEmailUser => ({
  id,
  email: `${id}@example.com`,
  role: 'user',
  status: 'active',
  username,
  lastUsedAt,
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('user last used dates', () => {
  it('defaults to today and yesterday in Asia/Shanghai', () => {
    expect(defaultSelectedDates(new Date('2026-08-12T16:30:00Z'))).toEqual([
      '2026-08-13',
      '2026-08-12',
    ])
  })

  it('deduplicates selected dates and keeps newest first', () => {
    expect(normalizeSelectedDates(['2026-08-11', 'invalid', '2026-08-13', '2026-08-11'])).toEqual([
      '2026-08-13',
      '2026-08-11',
    ])
  })

  it('groups by Shanghai natural day and keeps full second precision', () => {
    const groups = groupUsersByLastUsedDate([
      user('1', ' alice ', '2026-08-12T16:00:01Z'),
      user('2', 'bob', '2026-08-12T15:59:59Z'),
      user('3', '', '2026-08-13T01:00:00Z'),
      user('4', 'ignored-null', null),
      user('5', 'ignored-old', '2026-08-10T01:00:00Z'),
      user('1', 'duplicate-alice', '2026-08-12T16:00:01Z'),
    ], ['2026-08-13', '2026-08-12'])

    expect(groups.map((group) => [group.date, group.items.map((item) => item.username)])).toEqual([
      ['2026-08-13', ['alice']],
      ['2026-08-12', ['bob']],
    ])
    expect(groups[0].items[0].displayLastUsedAt).toBe('2026-08-13 00:00:01')
    expect(dateKeyInTimeZone('2026-08-12T16:00:00Z')).toBe('2026-08-13')
    expect(formatLastUsedAt('2026-08-12T15:59:59Z')).toBe('2026-08-12 23:59:59')
  })

  it('stops only after crossing the earliest date or reaching the null tail', () => {
    expect(pageContainsDateBefore([
      user('1', 'alice', '2026-08-12T12:00:00Z'),
      user('2', 'bob', '2026-08-10T12:00:00Z'),
    ], '2026-08-12')).toBe(true)
    expect(pageContainsDateBefore([user('1', 'alice', '2026-08-12T12:00:00Z')], '2026-08-12')).toBe(false)
    expect(pageIsLastUsedTimeTail([user('1', 'alice', null), user('2', 'bob', 'not-a-time')])).toBe(true)
    expect(pageIsLastUsedTimeTail([user('1', 'alice', '2026-08-12T12:00:00Z')])).toBe(false)
  })
})

describe('user last used view contract', () => {
  it('queries only last_used_at and copies each row username', () => {
    expect(viewSource).toContain("sortBy: 'last_used_at'")
    expect(viewSource).toContain("sortOrder: 'desc'")
    expect(viewSource).toContain('navigator.clipboard.writeText(username)')
    expect(viewSource).toContain('@click="copyUsername(user.id, user.username)"')
    expect(viewSource).not.toContain('last_active_at')
  })

  it('distinguishes upstream admin auth from TransitHub login expiry', () => {
    expect(viewSource).toContain("message === 'admin.massEmail.errors.upstreamAuth'")
    expect(viewSource).toContain("'admin.userLastUsed.errors.upstreamAuth'")
  })
})

describe('user last used API errors', () => {
  it('does not clear TransitHub login for an upstream admin auth error', async () => {
    const removeItem = vi.fn()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => 'transithub-token'),
      removeItem,
      setItem: vi.fn(),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      message: 'admin.massEmail.errors.upstreamAuth',
    }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    })))

    await expect(listMassEmailUsers({
      page: 1,
      pageSize: 100,
      status: '',
      role: '',
      search: '',
      sortBy: 'last_used_at',
      sortOrder: 'desc',
      timezone: 'Asia/Shanghai',
    }, { preserveUpstreamAuthError: true })).rejects.toThrow('admin.massEmail.errors.upstreamAuth')
    expect(removeItem).not.toHaveBeenCalled()
  })
})
