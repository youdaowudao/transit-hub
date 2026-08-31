// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import TestQuestionsPanel from '@/modules/admin/components/settings/TestQuestionsPanel.vue'

const harness = vi.hoisted(() => ({
  listTestQuestions: vi.fn(),
  createTestQuestion: vi.fn(),
  updateTestQuestion: vi.fn(),
  setTestQuestionEnabled: vi.fn(),
  setDefaultTestQuestion: vi.fn(),
  deleteTestQuestion: vi.fn(),
}))

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  listTestQuestions: harness.listTestQuestions,
  createTestQuestion: harness.createTestQuestion,
  updateTestQuestion: harness.updateTestQuestion,
  setTestQuestionEnabled: harness.setTestQuestionEnabled,
  setDefaultTestQuestion: harness.setDefaultTestQuestion,
  deleteTestQuestion: harness.deleteTestQuestion,
}))

const configuredQuestion = {
  id: 'question-configured',
  name: '边界检查',
  body: '请回答边界条件',
  keywords: ['错误码', 'Error'],
  enabled: true,
  isDefault: true,
  createdAt: '2026-08-31T00:00:00Z',
  updatedAt: '2026-08-31T00:00:00Z',
}

const emptyQuestion = {
  ...configuredQuestion,
  id: 'question-empty',
  name: '无关键字问题',
  keywords: [],
  isDefault: false,
}

const wrappers: VueWrapper[] = []

const mountPanel = async () => {
  const wrapper = mount(TestQuestionsPanel)
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const saveButton = (wrapper: VueWrapper) => {
  const button = wrapper.findAll('button').find(item => (
    item.text().includes('新增问题') || item.text().includes('保存修改')
  ))
  if (!button) throw new Error('missing question save button')
  return button
}

beforeEach(() => {
  harness.listTestQuestions.mockReset().mockResolvedValue([configuredQuestion, emptyQuestion])
  harness.createTestQuestion.mockReset().mockResolvedValue(configuredQuestion)
  harness.updateTestQuestion.mockReset().mockResolvedValue(configuredQuestion)
  harness.setTestQuestionEnabled.mockReset().mockResolvedValue(configuredQuestion)
  harness.setDefaultTestQuestion.mockReset().mockResolvedValue(configuredQuestion)
  harness.deleteTestQuestion.mockReset().mockResolvedValue(undefined)
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('connection health question keyword settings', () => {
  it('parses pasted keywords, submits first-spelling order and shows saved or empty values', async () => {
    const wrapper = await mountPanel()

    await wrapper.get('#test-question-name').setValue('新边界检查')
    await wrapper.get('#test-question-body').setValue('请回答新的边界条件')
    await wrapper.get('#test-question-keywords').setValue(' 错误码,Error\nerror\r\n[done] ')

    expect(wrapper.get('[data-testid="test-question-keyword-count"]').text()).toContain('3/20')
    expect(wrapper.get('[data-testid="test-question-keyword-bytes"]').text()).toContain('/2048')
    await saveButton(wrapper).trigger('click')
    await flushPromises()

    expect(harness.createTestQuestion).toHaveBeenCalledWith({
      name: '新边界检查',
      body: '请回答新的边界条件',
      keywords: ['错误码', 'Error', '[done]'],
    })
    expect(wrapper.get<HTMLTextAreaElement>('#test-question-keywords').element.value).toBe('')
    expect(wrapper.text()).toContain('错误码')
    expect(wrapper.text().indexOf('错误码')).toBeLessThan(wrapper.text().indexOf('Error'))
    expect(wrapper.text()).toContain('未配置关键字')
  })

  it('fills edit values, sends explicit empty to clear, and cancel clears local keyword input', async () => {
    const wrapper = await mountPanel()
    const row = wrapper.findAll('li').find(item => item.text().includes(configuredQuestion.name))
    if (!row) throw new Error('missing configured question row')
    await row.get('button[title="编辑"]').trigger('click')

    expect(wrapper.get<HTMLTextAreaElement>('#test-question-keywords').element.value).toBe('错误码\nError')
    await wrapper.get('#test-question-keywords').setValue('')
    await saveButton(wrapper).trigger('click')
    await flushPromises()
    expect(harness.updateTestQuestion).toHaveBeenCalledWith(configuredQuestion.id, {
      name: configuredQuestion.name,
      body: configuredQuestion.body,
      keywords: [],
    })

    const refreshedRow = wrapper.findAll('li').find(item => item.text().includes(configuredQuestion.name))
    if (!refreshedRow) throw new Error('missing refreshed question row')
    await refreshedRow.get('button[title="编辑"]').trigger('click')
    expect(wrapper.get<HTMLTextAreaElement>('#test-question-keywords').element.value).toBe('错误码\nError')
    await wrapper.get('button[title="取消编辑"]').trigger('click')
    expect(wrapper.get<HTMLTextAreaElement>('#test-question-keywords').element.value).toBe('')
  })

  it('keeps the keyword textarea after a save failure', async () => {
    harness.createTestQuestion.mockRejectedValueOnce(new Error('admin.connectionHealth.errors.request'))
    const wrapper = await mountPanel()
    await wrapper.get('#test-question-name').setValue('失败保留')
    await wrapper.get('#test-question-body').setValue('失败后不能清空')
    await wrapper.get('#test-question-keywords').setValue('错误码\nError')

    await saveButton(wrapper).trigger('click')
    await flushPromises()

    expect(wrapper.get<HTMLTextAreaElement>('#test-question-keywords').element.value).toBe('错误码\nError')
    expect(wrapper.text()).toContain('操作失败')
  })

  it('disables save for 21 items, 65 code points or 2049 UTF-8 bytes', async () => {
    const wrapper = await mountPanel()
    await wrapper.get('#test-question-name').setValue('容量边界')
    await wrapper.get('#test-question-body').setValue('容量边界正文')
    const keywordInput = wrapper.get('#test-question-keywords')

    await keywordInput.setValue(Array.from({ length: 21 }, (_, index) => `keyword-${index}`).join('\n'))
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    await keywordInput.setValue('界'.repeat(65))
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    const exactBytes = ['😀', '😁', '😂', '😃', '😄', '😅', '😆', '😉']
      .map(value => value.repeat(64))
    await keywordInput.setValue([...exactBytes, 'x'].join('\n'))
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    await keywordInput.setValue(exactBytes.join('\n'))
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('reports an overlong item before the deduplicated count limit for combined invalid input', async () => {
    const wrapper = await mountPanel()
    await wrapper.get('#test-question-name').setValue('组合容量边界')
    await wrapper.get('#test-question-body').setValue('组合容量边界正文')
    await wrapper.get('#test-question-keywords').setValue([
      '界'.repeat(65),
      ...Array.from({ length: 20 }, (_, index) => `keyword-${index}`),
    ].join('\n'))

    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('每个关键字最多 64 个字符。')
    expect(wrapper.text()).not.toContain('关键字最多 20 个。')
  })
})
