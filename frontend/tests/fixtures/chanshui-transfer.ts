export const resultContentType = 'application/vnd.transithub.chanshui-result+json'
export const cleanupWarning = { message: '运行设置已保存，但检测读取程序未能确认退出。请核查运行状态，不要重复保存。', detail: 'systemd 查询检测读取程序失败：控制连接已断开。', step: 'query-exit', operationResult: 'unknown', retryable: true }
export function resultEnvelope(value: unknown, contentType = 'application/json', extra: Record<string, unknown> = {}) {
  const bytes = ArrayBuffer.isView(value) ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength) : new TextEncoder().encode(JSON.stringify(value))
  return { v: 1, contentType, length: bytes.length, payloadBase64: Buffer.from(bytes).toString('base64'), complete: true, ...extra }
}
export function resultResponse(value: unknown, contentType = 'application/json', extra: Record<string, unknown> = {}) {
  return new Response(JSON.stringify(resultEnvelope(value, contentType, extra)), { headers: { 'Content-Type': resultContentType } })
}
