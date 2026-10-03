/**
 * 静态中文翻译助手（替代 vue-i18n）
 *
 * 提供与 vue-i18n Composition API 同名的符号：
 *   t(key, params?)  — 按 key 查值，支持 {param} 占位符插值
 *   te(key)          — 检查 key 是否存在
 *   locale           — 构建期格式化语言；当前文案回退中文
 *
 * 原来每个组件里的 `const { t } = useI18n()` 替换为
 * `import { t } from '@/locales'`，其余调用方式保持不变。
 */

import messages from './zh'

type NestedRecord = { [k: string]: string | NestedRecord }

// 把嵌套对象展平为 dot-key → string 映射
function flatten(obj: NestedRecord, prefix = ''): Record<string, string> {
  const result: Record<string, string> = {}
  for (const [k, v] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'string') {
      result[fullKey] = v
    } else {
      Object.assign(result, flatten(v as NestedRecord, fullKey))
    }
  }
  return result
}

const flat = flatten(messages as unknown as NestedRecord)

export type AppLocale = 'zh' | 'en' | `zh-${string}` | `en-${string}`

/** 唯一语言入口；当前仅有中文文案，英文配置用于数字和日期格式。 */
export function resolveAppLocale(value: unknown): AppLocale {
  if (typeof value !== 'string' || !value.trim()) return 'zh'
  try {
    const [canonical] = Intl.getCanonicalLocales(value.trim())
    const language = new Intl.Locale(canonical).language
    return language === 'zh' || language === 'en' ? canonical as AppLocale : 'zh'
  } catch {
    return 'zh'
  }
}

export const locale = resolveAppLocale(import.meta.env.VITE_APP_LOCALE)
export const messageLocale = 'zh'

/**
 * 翻译函数
 * @param key    dot-notation 翻译键，或运行时拼接的动态键
 * @param params 可选插值参数，替换字符串内 {paramName} 占位符
 */
export function t(key: string, params?: Record<string, string | number>): string {
  const raw = flat[key] ?? key
  if (!params) return raw
  return raw.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? `{${k}}`))
}

/**
 * 检查翻译键是否存在（对应 vue-i18n 的 te()）
 */
export function te(key: string): boolean {
  return Object.prototype.hasOwnProperty.call(flat, key)
}
