import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { listMassEmailUsers, listSelfRechargeUsers } from '@/modules/admin/api/massEmail'
import type { MassEmailUser } from '@/modules/admin/types/massEmail'
import {
  copiedUserKey,
  dateKeyInTimeZone,
  defaultSelectedDates,
  formatLastUsedAt,
  groupUsersByLastUsedDate,
  normalizeSelectedDates,
  pageContainsDateBefore,
  pageIsLastUsedTimeTail,
  parseCopiedUserKeys,
} from '@/modules/admin/utils/userLastUsed'

const viewSource = readFileSync(
  fileURLToPath(new URL('../src/modules/admin/views/UserLastUsedView.vue', import.meta.url)),
  'utf8',
)
const routerSource = readFileSync(
  fileURLToPath(new URL('../src/router.ts', import.meta.url)),
  'utf8',
)

const user = (
  id: string,
  email: string | null,
  lastUsedAt: string | null,
  username: string | null = null,
): MassEmailUser => ({
  id,
  email: email ?? '',
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
      user('1', ' alice@example.com ', '2026-08-12T16:00:01Z'),
      user('2', 'bob@example.com', '2026-08-12T15:59:59Z'),
      user('3', '', '2026-08-13T01:00:00Z', 'has-username'),
      user('4', 'ignored-null@example.com', null),
      user('5', 'ignored-old@example.com', '2026-08-10T01:00:00Z'),
      user('1', 'duplicate-alice@example.com', '2026-08-12T16:00:01Z'),
    ], ['2026-08-13', '2026-08-12'])

    expect(groups.map((group) => [group.date, group.items.map((item) => item.email)])).toEqual([
      ['2026-08-13', ['alice@example.com']],
      ['2026-08-12', ['bob@example.com']],
    ])
    expect(groups[0].items[0].displayLastUsedAt).toBe('2026-08-13 00:00:01')
    expect(dateKeyInTimeZone('2026-08-12T16:00:00Z')).toBe('2026-08-13')
    expect(formatLastUsedAt('2026-08-12T15:59:59Z')).toBe('2026-08-12 23:59:59')
  })

  it('stops only after crossing the earliest date or reaching the null tail', () => {
    expect(pageContainsDateBefore([
      user('1', 'alice@example.com', '2026-08-12T12:00:00Z'),
      user('2', 'bob@example.com', '2026-08-10T12:00:00Z'),
    ], '2026-08-12')).toBe(true)
    expect(pageContainsDateBefore([user('1', 'alice@example.com', '2026-08-12T12:00:00Z')], '2026-08-12')).toBe(false)
    expect(pageIsLastUsedTimeTail([user('1', 'alice@example.com', null), user('2', 'bob@example.com', 'not-a-time')])).toBe(true)
    expect(pageIsLastUsedTimeTail([user('1', 'alice@example.com', '2026-08-12T12:00:00Z')])).toBe(false)
  })
})

describe('user last used copied state', () => {
  it('normalizes email and safely restores persisted copied users', () => {
    const key = copiedUserKey(' user-1 ', ' Alice@Example.com ')
    expect(key).toBe('["user-1","alice@example.com"]')
    expect([...parseCopiedUserKeys(JSON.stringify([key, '', 123, key]))]).toEqual([key])
    expect(parseCopiedUserKeys('{invalid')).toEqual(new Set())
    expect(parseCopiedUserKeys(JSON.stringify({ key }))).toEqual(new Set())
  })
})

describe('user last used view contract', () => {
  it('queries only last_used_at and keeps copied email rows marked without replacing the copy button', () => {
    expect(viewSource).toContain("sortBy: 'last_used_at'")
    expect(viewSource).toContain("sortOrder: 'desc'")
    expect(viewSource).toContain('navigator.clipboard.writeText(email)')
    expect(viewSource).toContain('@click="copyEmail(user.id, user.email)"')
    expect(viewSource).toContain('window.localStorage.setItem(USER_LAST_USED_COPIED_STORAGE_KEY')
    expect(viewSource).toContain('const isCopied = (userId: string, email: string)')
    expect(viewSource).toContain('bg-emerald-500/10 hover:bg-emerald-500/15')
    expect(viewSource).toContain("{{ t('admin.userLastUsed.copied') }}")
    expect(viewSource).not.toContain('setTimeout')
    expect(viewSource).not.toContain('<Check')
    expect(viewSource).not.toContain('user.username')
    expect(viewSource).not.toContain('last_active_at')
  })

  it('adds recharge records to the existing page without a new route or username display', () => {
    expect(viewSource).toContain("type QueryMode = 'lastUsed' | 'recharge'")
    expect(viewSource).toContain("if (mode === 'recharge' && !rechargeLoaded.value) void loadRechargeUsers()")
    expect(viewSource).toContain("@click=\"copyEmail(user.userId, user.email)\"")
    expect(viewSource).toContain("t('admin.userRecharge.totalAmount')")
    expect(viewSource).not.toContain('user.username')
    expect(routerSource.match(/path: 'user-last-used'/g)).toHaveLength(1)
    expect(routerSource).not.toContain('user-recharge')
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

  it('queries the self-recharge aggregate with a cancellable request', async () => {
		vi.stubGlobal('localStorage', { getItem: vi.fn(() => null), removeItem: vi.fn(), setItem: vi.fn() })
    const controller = new AbortController()
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBe(controller.signal)
      return new Response(JSON.stringify({
        items: [{
          userId: '42',
          email: 'payer@example.com',
          rechargeCount: 2,
          totalAmount: 10,
          lastRechargedAt: '2026-08-13T04:05:06Z',
        }],
        totalUsers: 1,
        totalRecords: 2,
        totalAmount: 10,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)

    const result = await listSelfRechargeUsers({ signal: controller.signal })
    expect(fetchMock).toHaveBeenCalledWith('/api/mass-email/self-recharge-users', expect.any(Object))
    expect(result.items[0].email).toBe('payer@example.com')
    expect(result.totalRecords).toBe(2)
  })

  it('preserves AbortError so the page can ignore superseded queries', async () => {
		vi.stubGlobal('localStorage', { getItem: vi.fn(() => null), removeItem: vi.fn(), setItem: vi.fn() })
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new DOMException('aborted', 'AbortError')
    }))

    await expect(listSelfRechargeUsers()).rejects.toMatchObject({ name: 'AbortError' })
  })
})
