import { flushPromises, type DOMWrapper, type VueWrapper } from '@vue/test-utils'

/**
 * 新版批量抽屉每次打开都不勾选分组。旧用例原先靠偏好里的 batchTargetIds 自动恢复账号；
 * 这里按同一份账号清单在界面上勾选分组、再取消清单以外的账号，得到相同的选中集合。
 */
export const selectBatchTargets = async (wrapper: VueWrapper | DOMWrapper<Element>, targetIds: string[]): Promise<void> => {
  const wanted = new Set(targetIds)
  for (const box of wrapper.findAll('input[type="checkbox"][data-group-id]')) {
    const input = box.element as HTMLInputElement
    if (input.disabled || input.checked) continue
    await box.trigger('change')
    await flushPromises()
  }
  for (const box of wrapper.findAll('input[type="checkbox"][data-target-id]')) {
    const input = box.element as HTMLInputElement
    const id = box.attributes('data-target-id') ?? ''
    if (input.checked && !wanted.has(id)) {
      await box.trigger('change')
      await flushPromises()
    }
  }
  // 没有任何清单账号的分组取消勾选，保持“已选分组”与旧选择一致。
  for (const box of wrapper.findAll('input[type="checkbox"][data-group-id]')) {
    const input = box.element as HTMLInputElement
    if (!input.checked) continue
    const container = box.element.closest('div')
    const members = container ? [...container.querySelectorAll('input[data-target-id]')].map(item => item.getAttribute('data-target-id') ?? '') : []
    if (!members.some(id => wanted.has(id))) {
      await box.trigger('change')
      await flushPromises()
    }
  }
}

/** 勾选所有可选分组（其账号默认全选），对应旧用例“保存了全部账号”的情形。 */
export const selectAllBatchTargets = async (wrapper: VueWrapper | DOMWrapper<Element>): Promise<void> => {
  await flushPromises()
  for (const box of wrapper.findAll('input[type="checkbox"][data-group-id]')) {
    const input = box.element as HTMLInputElement
    if (input.disabled || input.checked) continue
    await box.trigger('change')
    await flushPromises()
  }
}
