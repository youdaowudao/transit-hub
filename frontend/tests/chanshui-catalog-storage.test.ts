// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { acceptChanshuiCatalog, chanshuiCatalog, chanshuiCatalogSession, chanshuiCatalogStorageKey, clearChanshuiCatalogForSession, clearChanshuiCatalogForWorkspace, restoreChanshuiCatalog, validateChanshuiCatalog } from '../src/modules/admin/composables/chanshuiCatalog'
import { connectionHealthPreferencesStorageKey, createDefaultConnectionHealthPreferences } from '../src/modules/admin/utils/connectionHealthPreferences'

const account = (id = '7', priority?: number) => ({ targetId: `sub2api:w:${id}`, accountId: id, accountName: id, priority })
const fixture = () => ({ workspaceId: 'w', groups: [{ id: 'g', name: '组', accounts: [account()] }] })
const accept = (value: unknown) => acceptChanshuiCatalog('w', value, chanshuiCatalogSession(), () => true)
beforeEach(() => { clearChanshuiCatalogForSession(); localStorage.clear() })
afterEach(() => { vi.restoreAllMocks(); clearChanshuiCatalogForSession(); localStorage.clear() })

it('只保留名单白名单，附带的凭据、地址和健康字段不能存储或发送', () => {
  const value = fixture() as any
  value.token = 'private'; value.groups[0].session = 'private'; value.groups[0].accounts[0].key = 'private'; value.groups[0].accounts[0].baseURL = 'private'
  expect(accept(value)?.catalog).toEqual(fixture())
  expect(localStorage.getItem(chanshuiCatalogStorageKey('w'))).not.toContain('private')
})

it.each(['wrong workspace', 'empty group', 'duplicate group', 'wrong account', 'wrong target', 'duplicate account', 'bad flag', 'failed group', 'bad priority', 'bad protocol', 'bad models'])('损坏记录%s整份拒绝，不伪装成功空名单', kind => {
  const value: any = fixture()
  const group = value.groups[0], member = group.accounts[0]
  if (kind === 'wrong workspace') value.workspaceId = 'other'
  if (kind === 'empty group') group.id = ' '
  if (kind === 'duplicate group') value.groups.push({ ...group })
  if (kind === 'wrong account') member.accountId = '8'
  if (kind === 'wrong target') member.targetId = 'sub2api:other:7'
  if (kind === 'duplicate account') group.accounts.push({ ...member })
  if (kind === 'bad flag') group.accountsError = 'false'
  if (kind === 'failed group') group.accountsError = true
  if (kind === 'bad priority') member.priority = Infinity
  if (kind === 'bad protocol') member.protocol = 'unknown'
  if (kind === 'bad models') member.models = []
  expect(() => validateChanshuiCatalog(value, 'w')).toThrow('格式或归属不正确')
  localStorage.setItem(chanshuiCatalogStorageKey('w'), JSON.stringify(value))
  // Infinity serializes as null, which must also be rejected.
  expect(restoreChanshuiCatalog('w')).toBeNull()
  expect(localStorage.getItem(chanshuiCatalogStorageKey('w'))).toBeNull()
})

it('保留组序和隐藏偏好，新分组附后，账号按数字Priority排序，跨组账号允许重复', () => {
  const preferences = { ...createDefaultConnectionHealthPreferences(), groupOrder: ['z', 'g'], hiddenGroupIds: ['hide'] }
  localStorage.setItem(connectionHealthPreferencesStorageKey('w'), JSON.stringify(preferences))
  const value = { workspaceId: 'w', groups: [
    { id: 'g', name: 'G', accounts: [account('9', 9), account('nil'), account('7', 1)] },
    { id: 'z', name: 'Z', accounts: [account('7', 1)] },
    { id: 'hide', name: '隐藏', accounts: [] },
  ] }
  expect(accept(value)?.catalog.groups.map(group => group.id)).toEqual(['z', 'g'])
  expect(chanshuiCatalog.value?.groups[1]?.accounts.map(account => account.accountId)).toEqual(['7', '9', 'nil'])
  expect(accept({ ...value, groups: [...value.groups].reverse().concat({ id: 'new', name: '新', accounts: [] }) })?.catalog.groups.map(group => group.id)).toEqual(['z', 'g', 'new'])
})

it('存储写入失败保留内存，直接更新同内容可以再次保存并清除警告', () => {
  const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => { throw new DOMException('full', 'QuotaExceededError') })
  expect(accept(fixture())?.warning).toContain('上次保存的旧名单')
  expect(chanshuiCatalog.value).toEqual(fixture())
  expect(accept(fixture())?.warning).toBe('')
  expect(write).toHaveBeenCalledTimes(2)
  expect(JSON.parse(localStorage.getItem(chanshuiCatalogStorageKey('w'))!)).toEqual(fixture())
})

it('空目录是合法记录，切工作区只恢复对应记录，旧会话和已取消结果不能覆盖或保存', () => {
  accept(fixture())
  clearChanshuiCatalogForWorkspace('another')
  expect(restoreChanshuiCatalog('another')).toBeNull()
  expect(restoreChanshuiCatalog('w')).toEqual(fixture())
  const old = chanshuiCatalogSession(); clearChanshuiCatalogForSession()
  expect(acceptChanshuiCatalog('w', fixture(), old, () => true)).toBeNull()
  expect(acceptChanshuiCatalog('w', fixture(), chanshuiCatalogSession(), () => false)).toBeNull()
  expect(chanshuiCatalog.value).toBeNull()
  expect(accept({ workspaceId: 'w', groups: [] })?.catalog.groups).toEqual([])
  clearChanshuiCatalogForWorkspace('another')
  expect(restoreChanshuiCatalog('w')?.groups).toEqual([])
})

it('本功能记录读取异常不触碰认证或旧记录，损坏记录删不掉仍可读取新名单替换', () => {
  const key = chanshuiCatalogStorageKey('w')
  localStorage.setItem(key, JSON.stringify(fixture()))
  const originalRead = Storage.prototype.getItem
  const read = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (this: Storage, name: string) {
    if (name === key) throw new Error('catalog storage unavailable')
    return originalRead.call(this, name)
  })
  expect(restoreChanshuiCatalog('w')).toBeNull()
  read.mockRestore()
  expect(JSON.parse(localStorage.getItem(key)!)).toEqual(fixture())
  localStorage.setItem(key, '{broken')
  vi.spyOn(Storage.prototype, 'removeItem').mockImplementationOnce(() => { throw new Error('delete unavailable') })
  expect(restoreChanshuiCatalog('w')).toBeNull()
  expect(accept(fixture())?.warning).toBe('')
  expect(JSON.parse(localStorage.getItem(key)!)).toEqual(fixture())
})
