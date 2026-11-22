// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import type { Component } from 'vue'
import CampaignDetailDrawer from '@/modules/admin/components/group-rate-campaigns/CampaignDetailDrawer.vue'
import Sub2apiUserProfileModal from '@/modules/admin/components/tickets/Sub2apiUserProfileModal.vue'

// 旧写法把面板动画嵌在外层 v-if 里，面板和内层 Transition 同时创建，Vue 不播放首次进场，
// 抽屉滑入、弹窗放大从未生效。现在面板动画由外层同一次进出场驱动（data-overlay-panel）。
// 这里用真实 Teleport：测试工具的 Teleport 替身每次都会重建内部组件，进场动画不会触发。
const dialog = () => document.body.querySelector<HTMLElement>('[role="dialog"]')
let wrapper: VueWrapper | null = null

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  document.body.innerHTML = ''
})

describe('弹窗与抽屉进出场动画', () => {
  it.each<[string, Component, Record<string, unknown>, string]>([
    ['抽屉', CampaignDetailDrawer, { campaignId: null }, 'overlay-drawer'],
    ['弹窗', Sub2apiUserProfileModal, { ticketId: null }, 'overlay-modal'],
  ])('%s打开时面板跟随外层进场，关闭后立即移除', async (_name, component, props, name) => {
    wrapper = mount(component, { attachTo: document.body, props: { open: false, ...props }, global: { stubs: { Transition: false } } })
    expect(dialog()).toBeNull()

    await wrapper.setProps({ open: true })
    const panel = dialog()
    expect(panel?.hasAttribute('data-overlay-panel')).toBe(true)
    expect(panel?.closest(`.${name}-enter-from`)).not.toBeNull()
    expect(panel?.closest(`.${name}-enter-active`)).not.toBeNull()

    await vi.waitFor(() => expect(dialog()?.closest(`.${name}-enter-active`)).toBeNull())

    await wrapper.setProps({ open: false })
    await vi.waitFor(() => expect(dialog()).toBeNull())
  })
})
