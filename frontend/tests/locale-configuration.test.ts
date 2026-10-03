import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => { vi.unstubAllEnvs(); vi.resetModules() })

describe('configured locale with Chinese message fallback', () => {
  it.each(['zh', 'en', 'en-US', 'en-SG', 'zh-Hans'])('accepts %s formatting while retaining actual Chinese messages', async configured => {
    vi.stubEnv('VITE_APP_LOCALE', configured)
    const { locale, messageLocale, t } = await import('@/locales')
    expect(locale).toBe(configured)
    expect(messageLocale).toBe('zh')
    expect(t('admin.settings.save')).toBe('保存配置')
    const { formatAmount, formatDateTime } = await import('@/modules/admin/utils/dashboard')
    expect(formatAmount(100 * 7)).toBe('700.00')
    expect(formatAmount(null)).toBe('—')
    const timestamp = Date.parse('2026-10-02T16:30:00Z')
    expect(formatDateTime(timestamp)).toBe(new Intl.DateTimeFormat(configured, {
      timeZone: 'Asia/Singapore', year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', hour12: false,
    }).format(new Date(timestamp)))
  })

  it('normalizes a valid locale code once for all formatters', async () => {
    vi.stubEnv('VITE_APP_LOCALE', ' en-us ')
    const { locale } = await import('@/locales')
    expect(locale).toBe('en-US')
  })

  it.each([undefined, '', 'not-a-locale', 'ja-JP', 'en<script>', 'en_US'])('falls back to general Chinese for invalid or unsupported configuration %s', async configured => {
    vi.stubEnv('VITE_APP_LOCALE', configured)
    const { locale, t } = await import('@/locales')
    expect(locale).toBe('zh')
    expect(t('admin.settings.save')).toBe('保存配置')
  })
})
