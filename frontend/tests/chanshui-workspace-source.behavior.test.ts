// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { useAdminAccounts } from '../src/modules/admin/composables/useAdminAccounts'

const api = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), switch: vi.fn(), update: vi.fn(), remove: vi.fn(), push: vi.fn(async () => undefined) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: api.push, currentRoute: { value: { name: 'Admin' } } }) }))
vi.mock('@/modules/admin/api/adminAccounts', () => ({ getCurrentAdminAccount: api.get, listAdminAccounts: api.list, switchAdminAccount: api.switch, updateAdminAccount: api.update, deleteAdminAccount: api.remove }))
const account = (id: string) => ({ id, displayName: id, platform: 'sub2api' })
afterEach(() => { localStorage.clear(); vi.clearAllMocks() })

it('无Host期间换登录，旧模块ref及迟到读取均不能给新登录确认工作区', async () => {
  const state = useAdminAccounts()
  localStorage.setItem(authTokenStorageKey, 'source-a')
  api.get.mockResolvedValueOnce(account('w'))
  await state.loadCurrentAccount()
  expect(state.isCurrentAccountSession('w')).toBe(true)
  expect(state.isCurrentAccountSession('other')).toBe(false)
  let release!: (value: unknown) => void
  api.get.mockReturnValueOnce(new Promise(resolve => { release = resolve }))
  const pending = state.loadCurrentAccount()
  localStorage.setItem(authTokenStorageKey, 'source-b')
  expect(state.currentAccount.value?.id).toBe('w')
  expect(state.isCurrentAccountSession('w')).toBe(false)
  release(account('w')); await pending
  expect(state.isCurrentAccountSession('w')).toBe(false)
  api.get.mockResolvedValueOnce(account('w')); await state.loadCurrentAccount()
  expect(state.isCurrentAccountSession('w')).toBe(true)
  expect(api.get).toHaveBeenCalledTimes(3)
})

it('切换、重命名、删除及失败沿用原请求，所有当前工作区赋值都带对应来源', async () => {
  const state = useAdminAccounts()
  localStorage.setItem(authTokenStorageKey, 'source-a')
  api.switch.mockResolvedValueOnce(account('one'))
  await state.switchAccount('one')
  expect(state.isCurrentAccountSession('one')).toBe(true)
  api.update.mockResolvedValueOnce({ ...account('one'), displayName: '改名' })
  await state.renameAccount('one', '改名')
  expect(state.isCurrentAccountSession('one')).toBe(true)
  expect(state.currentAccount.value?.displayName).toBe('改名')
  state.accounts.value = [account('one'), account('two')] as any
  api.remove.mockResolvedValueOnce({ deletedId: 'one', hasCurrent: true, currentAdminAccountId: 'two' })
  await state.deleteAccount('one', { confirmation: 'DELETE WORKSPACE' } as any)
  expect(state.isCurrentAccountSession('one')).toBe(false)
  expect(state.isCurrentAccountSession('two')).toBe(true)
  api.get.mockRejectedValueOnce(new Error('unavailable'))
  expect(await state.loadCurrentAccount()).toBe(false)
  expect(state.currentAccount.value).toBeNull()
  expect(state.isCurrentAccountSession('two')).toBe(false)
  expect(api.switch).toHaveBeenCalledTimes(1)
  expect(api.update).toHaveBeenCalledTimes(1)
  expect(api.remove).toHaveBeenCalledTimes(1)
})
