// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChanshuiError, requestChanshui } from '../src/modules/admin/api/chanshui'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { cleanupWarning, resultContentType, resultEnvelope, resultResponse } from './fixtures/chanshui-transfer'

const request = requestChanshui as (op: string, params: Record<string, unknown>, workspaceId: string, signal?: AbortSignal, onWarning?: (warning: Record<string, unknown>) => void) => Promise<any>
const serve = (response: Response) => vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
beforeEach(() => { localStorage.setItem(authTokenStorageKey, 'fixture-token') })
afterEach(() => { localStorage.removeItem(authTokenStorageKey); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('真实HTTP检测结果包及回收警告', () => {
  it('完整JSON返回保存值，完整PNG返回原图，不把外层结果包当业务值', async () => {
    serve(resultResponse({ revision: 4, memoryLimitMiB: 384 }))
    await expect(request('save-settings', { revision: 3 }, 'w')).resolves.toEqual({ revision: 4, memoryLimitMiB: 384 })
    const image = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])
    serve(resultResponse(image, 'image/png'))
    await expect(request('image', { reportId: 'r', itemId: 'i' }, 'w')).resolves.toEqual({ dataURL: `data:image/png;base64,${Buffer.from(image).toString('base64')}` })
  })
  it('完整结果的清理警告保留成功保存值和详细指引，无处理回调时明确报错', async () => {
    const onWarning = vi.fn(), value = { revision: 2, memoryLimitMiB: 384 }
    serve(resultResponse(value, 'application/json', { error: cleanupWarning }))
    await expect(request('save-settings', { revision: 1 }, 'w', undefined, onWarning)).resolves.toEqual(value)
    expect(onWarning).toHaveBeenCalledExactlyOnceWith(cleanupWarning)
    serve(resultResponse(value, 'application/json', { error: cleanupWarning }))
    const error = await request('save-settings', { revision: 1 }, 'w').catch(e => e)
    expect(error).toBeInstanceOf(ChanshuiError)
    expect(error.message).toContain('已保存')
    expect(error.details).toMatchObject(cleanupWarning)
  })
  it.each([
    ['未完成', { complete: false, error: { message: '只收到一部分结果，读取程序已中断。' } }],
    ['缺结束标记', { complete: undefined }],
    ['错误版本', { v: 2 }],
    ['错误长度', { length: 1 }],
    ['负长度', { length: -1 }],
    ['非整数长度', { length: 1.5 }],
    ['缺原长度', { length: undefined }],
    ['非法编码', { payloadBase64: '%%%%' }],
    ['截断编码', { payloadBase64: 'e30' }],
    ['非规范编码', { payloadBase64: 'e31=', length: 2 }],
    ['不支持内容', { contentType: 'text/html' }],
    ['图片错配', { contentType: 'image/png' }],
    ['无效警告', { error: { detail: '缺少具体原因' } }],
    ['过大元数据', { error: { message: '大'.repeat(24 * 1024) } }],
  ])('%s不把截断或损坏结果当成功', async (_label, changes) => {
    serve(resultResponse({ revision: 2 }, 'application/json', changes))
    const warning = vi.fn()
    await expect(request('save-settings', { revision: 1 }, 'w', undefined, warning)).rejects.toThrow(/结果|内容|传输/)
    expect(warning).not.toHaveBeenCalled()
  })
  it('完整标记不能掩盖坏JSON或已截断的外层，旧的业务值不会收到替代结果', async () => {
    serve(resultResponse(new TextEncoder().encode('{broken')))
    await expect(request('settings', {}, 'w')).rejects.toThrow(/结果|内容|传输/)
    const envelope = JSON.stringify(resultEnvelope({ revision: 2 }))
    serve(new Response(envelope.slice(0, -3), { headers: { 'Content-Type': resultContentType } }))
    await expect(request('settings', {}, 'w')).rejects.toThrow(/结果|内容|传输/)
  })
  it('图片原长度和真实传输均限制2MiB，读取流中断也明确失败', async () => {
    serve(resultResponse(new Uint8Array([137]), 'image/png', { length: 2 * 1024 * 1024 + 1 }))
    await expect(request('image', {}, 'w')).rejects.toThrow(/大小|超过/)
    serve(resultResponse(new Uint8Array(2 * 1024 * 1024 + 1), 'image/png'))
    await expect(request('image', {}, 'w')).rejects.toThrow(/大小|超过/)
    serve(new Response(new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode('{"v":1')); controller.error(new Error('fixture transfer interrupted')) } }), { headers: { 'Content-Type': resultContentType } }))
    await expect(request('settings', {}, 'w')).rejects.toThrow(/传输|完整/)
  })
  it('专用结果包的读取有上界，超大外层取消流并拒绝，不无限保留到内存', async () => {
    let sent = 0, cancelled = false
    serve(new Response(new ReadableStream({ pull(controller) { if (sent === 32) { controller.close(); return } sent++; controller.enqueue(new Uint8Array(128 * 1024)) }, cancel() { cancelled = true } }), { headers: { 'Content-Type': resultContentType } }))
    await expect(request('settings', {}, 'w')).rejects.toThrow(/大小|超过/)
    expect(cancelled).toBe(true); expect(sent).toBeLessThan(30)
  })
  it('特殊路由及前置错误继续兼容原平面JSON', async () => {
    serve(new Response(JSON.stringify({ enabled: true, available: true }), { headers: { 'Content-Type': 'application/json' } }))
    await expect(request('runtime', {}, 'w')).resolves.toEqual({ enabled: true, available: true })
    serve(new Response(JSON.stringify({ message: '题目已被修改，本次未覆盖，请重新读取。', step: 'save' }), { status: 409 }))
    const error = await request('save-question', {}, 'w').catch(e => e)
    expect(error.message).toContain('本次未覆盖'); expect(error.details.step).toBe('save')
  })
})
