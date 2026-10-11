// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminLayout from '@/modules/admin/layout/AdminLayout.vue'
import { getSystemVersion } from '@/modules/admin/api/system'

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  return {
    useAdminAccounts: () => ({
      currentAccount: ref(null),
      noticeKey: ref(''),
      loadCurrentAccount: vi.fn(async () => true),
    }),
  }
})
vi.mock('@/modules/admin/api/system', () => ({ getSystemVersion: vi.fn() }))

// 空业务页面避免启动业务请求或轮询。
const EmptyPage = defineComponent({ template: '<div />' })
const wrappers: VueWrapper[] = []
const githubRepoUrl = 'https://github.com/youdaowudao/transit-hub'

async function mountLayout() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin', component: EmptyPage },
      { path: '/admin/group-rates', component: EmptyPage },
      ...['upstream', 'group-associations', 'connection-health', 'settings'].map(path => ({
        path: `/admin/${path}`, component: EmptyPage,
      })),
    ],
  })
  await router.push('/admin')
  await router.isReady()
  const wrapper = mount(AdminLayout, { global: { plugins: [router] } })
  wrappers.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

beforeEach(() => vi.clearAllMocks())
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  localStorage.clear()
})

describe('页头新版本提醒', () => {
  it('有新版本时只显示灰色小字，保留原版本号和 GitHub 链接', async () => {
    vi.mocked(getSystemVersion).mockResolvedValue({ version: 'V2.9.11', updateAvailable: true })
    const { wrapper, router } = await mountLayout()
    const header = wrapper.get('header')
    expect(header.text()).toContain('有新版本')
    const notice = header.get('[data-testid="admin-update-available"]')
    expect(notice.text()).toBe('有新版本')
    expect(notice.element.closest('a, button')).toBeNull()
    expect(notice.attributes('title')).toBeUndefined()
    expect(notice.attributes('aria-label')).toBeUndefined()
    expect(notice.classes()).toEqual(expect.arrayContaining(['hidden', 'sm:inline', 'text-xs', 'text-muted-foreground']))
    expect(header.text().match(/v2\.9\.11/g)).toHaveLength(1)
    const links = header.findAll(`a[href="${githubRepoUrl}"]`)
    expect(links).toHaveLength(2)
    expect(links[0]?.text()).toBe('v2.9.11')
    for (const link of links) expect(link.attributes('target')).toBe('_blank')
    await notice.trigger('click')
    expect(router.currentRoute.value.path).toBe('/admin')
    expect(header.get('[data-testid="admin-update-available"]').text()).toBe('有新版本')
  })

  it.each([
    { version: 'V2.9.11', updateAvailable: false },
    { version: 'V2.9.11' },
  ])('没有新版本或旧后端缺失字段时不显示：%j', async response => {
    vi.mocked(getSystemVersion).mockResolvedValue(response)
    const { wrapper } = await mountLayout()
    expect(wrapper.get('header').text()).toContain('v2.9.11')
    expect(wrapper.get('header').text()).not.toContain('有新版本')
    expect(wrapper.find('[data-testid="admin-update-available"]').exists()).toBe(false)
  })

  it('版本请求失败时页面正常显示，不显示版本号或提醒', async () => {
    vi.mocked(getSystemVersion).mockRejectedValue(new Error('offline'))
    const { wrapper } = await mountLayout()
    expect(wrapper.get('header h1').text()).toBe('仪表盘')
    expect(wrapper.get('nav').text()).toContain('分组倍率')
    expect(wrapper.get('header').text()).not.toContain('有新版本')
    expect(wrapper.get('header').text()).not.toMatch(/v\d+\.\d+\.\d+/)
    expect(wrapper.find('[data-testid="admin-update-available"]').exists()).toBe(false)
    expect(wrapper.get('header').findAll(`a[href="${githubRepoUrl}"]`)).toHaveLength(1)
  })

  it('挂载只读取一次，路由切换不重读，也不增加定时器', async () => {
    vi.mocked(getSystemVersion).mockResolvedValue({ version: 'V2.9.11', updateAvailable: true })
    const interval = vi.spyOn(globalThis, 'setInterval')
    try {
      const { wrapper, router } = await mountLayout()
      expect(getSystemVersion).toHaveBeenCalledTimes(1)
      await router.push('/admin/group-rates')
      await flushPromises()
      expect(wrapper.get('header h1').text()).toBe('分组倍率')
      expect(wrapper.get('header').text()).toContain('有新版本')
      expect(getSystemVersion).toHaveBeenCalledTimes(1)
      expect(interval).not.toHaveBeenCalled()
    } finally {
      interval.mockRestore()
    }
  })
})
