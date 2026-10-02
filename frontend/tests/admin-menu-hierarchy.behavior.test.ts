// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AdminLayout from '@/modules/admin/layout/AdminLayout.vue'

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
vi.mock('@/modules/admin/api/system', () => ({
  getSystemVersion: vi.fn(async () => ({ version: 'V2.8.9' })),
}))

// 只挂载导航，空页面避免启动业务请求、轮询或真实服务。
const EmptyPage = defineComponent({ template: '<div />' })
const primaryEntries = [
  ['分组倍率', '/admin/group-rates'],
  ['调价映射', '/admin/group-associations'],
  ['分组健康', '/admin/connection-health'],
] as const
const secondaryEntries = [
  ['排行榜', '/admin/leaderboard'],
  ['抽奖活动', '/admin/lottery'],
  ['活动调价', '/admin/group-rate-campaigns'],
  ['工单', '/admin/tickets'],
  ['群发邮件', '/admin/mass-email'],
  ['用户最后使用', '/admin/user-last-used'],
] as const
const wrappers: VueWrapper[] = []

async function mountMenu(path = '/admin') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin', component: EmptyPage },
      ...[...primaryEntries, ...secondaryEntries].map(([, path]) => ({ path, component: EmptyPage })),
      { path: '/admin/upstream', component: EmptyPage },
      { path: '/admin/settings', component: EmptyPage },
      { path: '/admin/accounts', name: 'AdminAccounts', component: EmptyPage },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(AdminLayout, { global: { plugins: [router] } })
  wrappers.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  localStorage.clear()
})

describe('左侧菜单层级', () => {
  it('分组功能直接进入原页面，辅助功能只在二级菜单展开后出现', async () => {
    const { wrapper, router } = await mountMenu()
    const nav = wrapper.get('nav')
    expect(nav.findAll(':scope > a').map(link => link.text())).toEqual([
      '仪表盘', '上游管理', '分组倍率', '调价映射', '分组健康', '系统设置',
    ])
    expect(nav.text()).not.toContain('分组管理')
    expect(nav.text()).not.toContain('嵌入功能')
    expect(nav.findAll('button').map(button => button.text())).toEqual(['二级功能'])
    for (const [, path] of secondaryEntries) expect(nav.find(`a[href="${path}"]`).exists()).toBe(false)

    for (const [label, path] of primaryEntries) {
      await nav.get(`a[href="${path}"]`).trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.path).toBe(path)
      expect(wrapper.get('header h1').text()).toBe(label)
      expect(nav.findAll('a.bg-primary').map(link => link.text())).toEqual([label])
    }

    await nav.get('button').trigger('click')
    expect(nav.get('button').attributes('aria-expanded')).toBe('true')
    expect(nav.findAll(':scope > div a').map(link => link.text())).toEqual(secondaryEntries.map(([label]) => label))
    for (const [label, path] of secondaryEntries) {
      expect(nav.findAll(`a[href="${path}"]`)).toHaveLength(1)
      expect(nav.find(`:scope > a[href="${path}"]`).exists()).toBe(false)
      await nav.get(`a[href="${path}"]`).trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.path).toBe(path)
      expect(wrapper.get('header h1').text()).toBe(label)
      expect(nav.findAll('a.bg-primary').map(link => link.text())).toEqual([label])
    }
    await nav.get('button').trigger('click')
    expect(nav.get('button').attributes('aria-expanded')).toBe('false')
    expect(nav.get('button').classes()).toContain('text-primary')
    expect(nav.findAll(':scope > div a')).toHaveLength(0)
  })

  it.each(secondaryEntries.slice(-2))('%s 直接进入时默认展开，手动收起保持有效', async (label, path) => {
    const { wrapper, router } = await mountMenu(path)
    const nav = wrapper.get('nav')
    expect(nav.get('button').attributes('aria-expanded')).toBe('true')
    expect(nav.get(`a[href="${path}"]`).classes()).toContain('bg-primary')
    expect(wrapper.get('header h1').text()).toBe(label)
    await nav.get('button').trigger('click')
    await router.push(path === '/admin/mass-email' ? '/admin/user-last-used' : '/admin/mass-email')
    await flushPromises()
    expect(nav.get('button').attributes('aria-expanded')).toBe('false')
    expect(nav.findAll(':scope > div a')).toHaveLength(0)
  })

  it('手机导航点击五个移动入口后收起，工作区选择页继续隐藏业务导航', async () => {
    const { wrapper, router } = await mountMenu()
    const sidebar = wrapper.get('#admin-mobile-sidebar')
    const openButton = wrapper.get('header button[aria-controls="admin-mobile-sidebar"]')
    for (const [label, path] of [...primaryEntries, ...secondaryEntries.slice(-2)]) {
      await openButton.trigger('click')
      expect(openButton.attributes('aria-expanded')).toBe('true')
      expect(sidebar.classes()).toContain('translate-x-0')
      const group = wrapper.get('nav button')
      if (path === '/admin/mass-email' && group.attributes('aria-expanded') === 'false') await group.trigger('click')
      await sidebar.get(`a[href="${path}"]`).trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.path).toBe(path)
      expect(wrapper.get('header h1').text()).toBe(label)
      expect(openButton.attributes('aria-expanded')).toBe('false')
      expect(sidebar.classes()).toContain('-translate-x-full')
    }
    await router.push('/admin/accounts')
    await flushPromises()
    expect(wrapper.find('aside').exists()).toBe(false)
    expect(wrapper.find('header').exists()).toBe(false)
  })
})
