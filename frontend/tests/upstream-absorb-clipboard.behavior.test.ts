// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserActivityView from '@/modules/admin/views/UserActivityView.vue'
import VipView from '@/modules/admin/views/VipView.vue'
import { t } from '@/locales'
import { copyText } from '@/modules/admin/utils/clipboard'
import { account, activityList, activitySummary, vipSummary } from './fixtures/upstreamAbsorb'

const api = vi.hoisted(() => ({
  current: null as any,
  switching: null as any,
  summary: vi.fn(), users: vi.fn(), vip: vi.fn(), write: vi.fn(), exec: vi.fn(),
}))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  api.current = ref(null)
  api.switching = ref(false)
  return { useAdminAccounts: () => ({ currentAccount: api.current, isSwitching: api.switching, loadCurrentAccount: vi.fn() }) }
})
vi.mock('@/modules/admin/api/userActivity', () => ({
  getActivitySummary: api.summary, getActivityUsers: api.users,
  getActivityDetail: vi.fn(), getActivitySync: vi.fn(), startActivitySync: vi.fn(),
}))
vi.mock('@/modules/admin/api/vip', () => ({
  getVipSummary: api.vip, searchVipUsers: vi.fn(), watchVipUser: vi.fn(),
  unwatchVipUser: vi.fn(), restoreVipRecommendations: vi.fn(), saveVipSettings: vi.fn(),
}))
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
const originalExec = Object.getOwnPropertyDescriptor(document, 'execCommand')
const clipboard = (value: unknown) => Object.defineProperty(navigator, 'clipboard', { configurable: true, value })
const wrappers: VueWrapper[] = []
const hosts: HTMLElement[] = []
const pages = [
  { name: 'activity', component: UserActivityView, value: 'first@example.test', locale: 'admin.userActivity', icon: true },
  { name: 'VIP', component: VipView, value: 'Fixture copied reminder', locale: 'admin.vip', icon: false },
] as const
beforeEach(() => {
  vi.resetAllMocks()
  api.current.value = account()
  api.switching.value = false
  api.summary.mockResolvedValue(activitySummary())
  api.users.mockResolvedValue(activityList())
  api.vip.mockResolvedValue(vipSummary())
  api.write.mockResolvedValue(undefined)
  clipboard({ writeText: api.write })
  Object.defineProperty(document, 'execCommand', { configurable: true, value: api.exec })
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  for (const host of hosts.splice(0)) host.remove()
  if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard)
  else Reflect.deleteProperty(navigator, 'clipboard')
  if (originalExec) Object.defineProperty(document, 'execCommand', originalExec)
  else Reflect.deleteProperty(document, 'execCommand')
  vi.restoreAllMocks()
})

describe.each(pages)('$name clipboard feedback', page => {
  const setup = async () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    hosts.push(host)
    const wrapper = mount(page.component, {
      attachTo: host,
      global: { stubs: { DashboardEChart: true, RouterLink: { template: '<a><slot /></a>' } } },
    })
    wrappers.push(wrapper)
    await flushPromises()
    const copy = page.icon
      ? wrapper.get(`button[aria-label="${t(page.locale + '.copy')}"]`)
      : wrapper.findAll('button').find(button => button.text() === t(page.locale + '.copy'))!
    return { wrapper, copy }
  }
  it('uses modern clipboard success without the legacy command', async () => {
    const { wrapper, copy } = await setup()
    await copy.trigger('click')
    await flushPromises()
    expect(api.write).toHaveBeenCalledExactlyOnceWith(page.value)
    expect(api.exec).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t(page.locale + '.copied'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copyError'))
  })
  it('retains the original error for modern rejection and never retries through legacy clipboard', async () => {
    api.write.mockRejectedValue(new Error('Fixture clipboard denied'))
    api.exec.mockReturnValue(true)
    const { wrapper, copy } = await setup()
    await copy.trigger('click')
    await flushPromises()
    expect(api.exec).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t(page.locale + '.copyError'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copied'))
  })
  it('copies on HTTP without navigator.clipboard and restores DOM and button focus', async () => {
    clipboard(undefined)
    const { wrapper, copy } = await setup()
    const originalTextareas = document.querySelectorAll('textarea').length
    const active = copy.element as HTMLButtonElement
    active.focus()
    api.exec.mockImplementation((command: string) => {
      expect(command).toBe('copy')
      expect(document.activeElement).toBeInstanceOf(HTMLTextAreaElement)
      const textarea = document.activeElement as HTMLTextAreaElement
      expect(textarea.value).toBe(page.value)
      expect(textarea.selectionStart).toBe(0)
      expect(textarea.selectionEnd).toBe(page.value.length)
      return true
    })
    await copy.trigger('click')
    await flushPromises()
    expect(api.exec).toHaveBeenCalledExactlyOnceWith('copy')
    expect(api.write).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t(page.locale + '.copied'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copyError'))
    expect(document.querySelectorAll('textarea')).toHaveLength(originalTextareas)
    expect(document.activeElement).toBe(active)
  })
  it('reports legacy false as failure and cleans temporary DOM and focus', async () => {
    clipboard(undefined)
    api.exec.mockReturnValue(false)
    const { wrapper, copy } = await setup()
    const originalTextareas = document.querySelectorAll('textarea').length
    const active = copy.element as HTMLButtonElement
    active.focus()
    await copy.trigger('click')
    await flushPromises()
    expect(api.exec).toHaveBeenCalledExactlyOnceWith('copy')
    expect(wrapper.text()).toContain(t(page.locale + '.copyError'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copied'))
    expect(document.querySelectorAll('textarea')).toHaveLength(originalTextareas)
    expect(document.activeElement).toBe(active)
  })
  it('preserves unrelated local read errors after a successful copy', async () => {
    const { wrapper, copy } = await setup()
    const failure = '读取本地资料失败；请重新读取。'
    if (page.name === 'activity') api.summary.mockRejectedValueOnce(new Error(failure))
    else api.vip.mockRejectedValueOnce(new Error(failure))
    const refreshKey = page.locale + '.refresh'
    const refresh = wrapper.findAll('button').find(button => button.text() === t(refreshKey))!
    await refresh.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain(failure)
    await copy.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain(t(page.locale + '.copied'))
    expect(wrapper.text()).toContain(failure)
  })
  it('clears stale copied/error feedback when a copy succeeds, fails, then recovers', async () => {
    const { wrapper, copy } = await setup()
    await copy.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain(t(page.locale + '.copied'))
    api.write.mockRejectedValueOnce(new Error('Fixture clipboard denied'))
    await copy.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain(t(page.locale + '.copyError'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copied'))
    await copy.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain(t(page.locale + '.copied'))
    expect(wrapper.text()).not.toContain(t(page.locale + '.copyError'))
  })
})

describe('clipboard utility cleanup and transport choice', () => {
  const focusFixture = () => {
    const button = document.createElement('button')
    document.body.appendChild(button)
    hosts.push(button)
    button.focus()
    return button
  }
  it('keeps modern method binding and leaves DOM and focus untouched', async () => {
    const active = focusFixture()
    const before = document.querySelectorAll('textarea').length
    const modern = { marker: 'fixture', writeText: vi.fn(function (this: { marker: string }, text: string) {
      expect(this.marker).toBe('fixture')
      expect(text).toBe('中文 emoji🙂')
      return Promise.resolve()
    }) }
    clipboard(modern)
    await expect(copyText('中文 emoji🙂')).resolves.toBeUndefined()
    expect(api.exec).not.toHaveBeenCalled()
    expect(document.querySelectorAll('textarea')).toHaveLength(before)
    expect(document.activeElement).toBe(active)
  })
  it('propagates modern rejection without falling back or moving focus', async () => {
    const active = focusFixture()
    const denial = new Error('Fixture permission denied')
    api.write.mockRejectedValue(denial)
    api.exec.mockReturnValue(true)
    await expect(copyText('fixture')).rejects.toBe(denial)
    expect(api.exec).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(active)
  })
  it('copies the complete Unicode string when writeText is missing', async () => {
    clipboard({})
    const active = focusFixture()
    const before = document.querySelectorAll('textarea').length
    api.exec.mockImplementation(() => {
      const textarea = document.activeElement as HTMLTextAreaElement
      expect(textarea.value).toBe('中文 emoji🙂\nsecond line')
      expect(textarea.readOnly).toBe(true)
      expect(textarea.selectionStart).toBe(0)
      expect(textarea.selectionEnd).toBe(textarea.value.length)
      return true
    })
    await expect(copyText('中文 emoji🙂\nsecond line')).resolves.toBeUndefined()
    expect(api.exec).toHaveBeenCalledExactlyOnceWith('copy')
    expect(document.querySelectorAll('textarea')).toHaveLength(before)
    expect(document.activeElement).toBe(active)
  })
  it.each([false, undefined, 1, 'true'])('rejects legacy result %s unless it is exactly true', async result => {
    clipboard(undefined)
    const active = focusFixture()
    const before = document.querySelectorAll('textarea').length
    api.exec.mockReturnValue(result)
    await expect(copyText('fixture')).rejects.toThrow(/未完成复制/)
    expect(document.querySelectorAll('textarea')).toHaveLength(before)
    expect(document.activeElement).toBe(active)
  })
  it('cleans DOM and focus when execCommand throws', async () => {
    clipboard(undefined)
    const active = focusFixture()
    const before = document.querySelectorAll('textarea').length
    const failure = new Error('Fixture legacy command blocked')
    api.exec.mockImplementation(() => { throw failure })
    await expect(copyText('fixture')).rejects.toBe(failure)
    expect(document.querySelectorAll('textarea')).toHaveLength(before)
    expect(document.activeElement).toBe(active)
  })
  it('reports an unavailable legacy command and still removes its temporary textarea', async () => {
    clipboard(undefined)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: undefined })
    const active = focusFixture()
    const before = document.querySelectorAll('textarea').length
    await expect(copyText('fixture')).rejects.toThrow(/未完成复制/)
    expect(document.querySelectorAll('textarea')).toHaveLength(before)
    expect(document.activeElement).toBe(active)
  })
})
