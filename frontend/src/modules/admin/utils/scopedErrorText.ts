import { getAccessToken } from '@/modules/auth/api/auth'
import { t, te } from '@/locales'

// These local-read errors contain only bounded display text. Never retain the
// original response or credentials while preserving a known cause and next step.
export const safeScopedErrorText = (value: unknown): string => {
  if (typeof value !== 'string') return ''
  let text = value
  const token = getAccessToken()
  if (token) text = text.split(token).join('[已遮盖]')
  text = text.replace(/[\u0000-\u001f\u007f]/g, '')
    .replace(/(?:https?|postgres(?:ql)?|redis|mysql):\/\/[^\s<>"'，。；]+/gi, raw => {
      try {
        const url = new URL(raw)
        if (!['http:', 'https:'].includes(url.protocol)) return '[地址已遮盖]'
        url.username = ''; url.password = ''; url.search = ''; url.hash = ''
        return url.toString()
      } catch { return '[地址已遮盖]' }
    })
    .replace(/\bBearer\s+[^\s,;，。；]+/gi, '[凭据已遮盖]')
    .replace(/(?:password|passwd|token|secret|api[_ -]?key|authorization|cookie|密码|密钥|令牌)\s*[:=]\s*[^\s,;，。；]+/gi, '[凭据已遮盖]')
    .replace(/[A-Za-z0-9]{24,}/g, '[已遮盖]')
  return text.trim().slice(0, 512)
}

export const scopedReadFailureText = (payload: unknown, fallback: string): string => {
  const record = payload && typeof payload === 'object' && !Array.isArray(payload)
    ? payload as Record<string, unknown> : {}
  let message = safeScopedErrorText(record.message)
  if (/^(?:admin|auth)\.[A-Za-z0-9_.]+$/.test(message)) message = te(message) ? t(message) : fallback
  return [message || fallback, safeScopedErrorText(record.detail), safeScopedErrorText(record.next)]
    .filter(Boolean).join('；')
}
