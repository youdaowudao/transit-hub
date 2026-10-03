import { describe, expect, it } from 'vitest'
import * as amountUtils from '../src/modules/admin/utils/dashboard'

const format = (amountUtils as unknown as Record<string, (value: number | null) => string>).formatAmount
  ?? (amountUtils as unknown as Record<string, (value: number | null) => string>).formatCny

describe('globalization amount presentation', () => {
  it('keeps the existing converted value and decimals without a currency marker', () => {
    expect(format(100 * 7)).toBe('700.00')
    expect(format(1234.5)).toBe('1,234.50')
  })
  it('distinguishes unknown, zero and negative amounts', () => {
    expect(format(null)).toBe('—')
    expect(format(0)).toBe('0.00')
    expect(format(-12.5)).toBe('-12.50')
  })
})
