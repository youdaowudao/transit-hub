// @vitest-environment jsdom

import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it } from 'vitest'

import StatCard from '@/modules/admin/components/dashboard/StatCard.vue'

const TestIcon = defineComponent({
  template: '<span aria-hidden="true" />',
})

const baseProps = {
  label: '今日总成本',
  value: '¥12.34',
  icon: TestIcon,
  color: 'primary' as const,
  deltaDirection: 'flat' as const,
  deltaText: '',
  deltaCaption: '较昨日',
}

describe('StatCard component behavior', () => {
  it('renders a button and emits one click event when clickable', async () => {
    const wrapper = mount(StatCard, {
      props: {
        ...baseProps,
        clickable: true,
      },
    })

    expect(wrapper.element.tagName).toBe('BUTTON')
    expect(wrapper.attributes('type')).toBe('button')

    await wrapper.trigger('click')

    expect(wrapper.emitted('click')).toHaveLength(1)
  })

  it('renders a div and emits no click event when not clickable', async () => {
    const wrapper = mount(StatCard, {
      props: baseProps,
    })

    expect(wrapper.element.tagName).toBe('DIV')
    expect(wrapper.attributes('type')).toBeUndefined()

    await wrapper.trigger('click')

    expect(wrapper.emitted('click')).toBeUndefined()
  })
})
