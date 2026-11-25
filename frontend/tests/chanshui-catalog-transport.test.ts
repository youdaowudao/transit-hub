// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { requestChanshui } from '../src/modules/admin/api/chanshui'

afterEach(() => { localStorage.clear(); vi.unstubAllGlobals() })

it('名单正文没有读完时用白话说明传输步骤和重试，不直接显示内部异常', async () => {
  localStorage.setItem(authTokenStorageKey, 'catalog-transport-fixture')
  const response = new Response('{}', { status: 200 })
  vi.spyOn(response, 'text').mockRejectedValueOnce(new Error('internal body stream exception'))
  vi.stubGlobal('fetch', vi.fn(async () => response))
  const error = await requestChanshui('catalog', {}, 'w').catch(error => error)
  expect(error.message).toContain('分组账号名单')
  expect(error.message).toContain('重试')
  expect(error.message).not.toMatch(/internal|exception|计费|报告/)
  expect(localStorage.getItem(authTokenStorageKey)).toBe('catalog-transport-fixture')
})
