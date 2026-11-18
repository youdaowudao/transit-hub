// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, expect, it, vi } from 'vitest'
import ResultCards from '../../chanshui/web/components/ResultCards'
import { I18nProvider } from '../../chanshui/web/lib/i18n'
import type { DetectVerdict } from '../../chanshui/web/lib/api'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react')
const { createRoot } = require('react-dom/client')

afterEach(() => vi.unstubAllGlobals())

it.each([
  ['openai/gpt-6.1-sol', 'gpt-6-astra', 1, false, true],
  ['gpt-6-astra', 'gpt-6.1-sol', .95, false, true],
  ['gpt-6-astra', 'gpt-6-astra', 1, false, true],
  ['gpt-6.1-sol', 'gpt-6-astra', .54, true, true],
  ['gpt-6.1-sol', 'gpt-6.2-astra', .95, false, false],
  ['gpt-6.2-sol', 'gpt-6-astra', .95, false, false],
])('renders the numeric verdict for %s and %s (%s, %s)', async (model, top, probability, low, unconfirmed) => {
  vi.stubGlobal('React', React)
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const verdict: DetectVerdict = {
    injection: null, hidden: null, thinking: null, iq: null,
    fingerprint: {
      family: 'gpt', family_name: 'GPT', family_prob: 1, basis: 'numeric',
      top_model: top, top_prob: probability, used_outputs: 3, sample_limit: 10,
      low_confidence: low, note: '', results: [{ model: top, probability, similarity: 1 }],
    },
  }
  const node = document.body.appendChild(document.createElement('div'))
  const root = createRoot(node)
  try {
    await React.act(async () => root.render(
      React.createElement(I18nProvider, { fixedLang: 'zh' }, React.createElement(ResultCards, { model, verdict })),
    ))
    expect(node.textContent?.includes(model === 'gpt-6-astra' ? 'Astra 未验明' : '型号未确认')).toBe(unconfirmed)
    expect(node.textContent?.includes('数字指纹接近 GPT-6 Astra 或 GPT-6.1 Sol')).toBe(unconfirmed)
    expect(node.querySelector('.fake-seal')?.textContent).toBe(unconfirmed ? undefined : '可疑')
    expect(node.querySelector('.candidate')?.textContent).toContain(top)
  } finally {
    await React.act(async () => root.unmount())
    node.remove()
  }
})
