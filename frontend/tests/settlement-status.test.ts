import { describe, it, expect } from 'vitest'

// 模拟结算状态类型
type SettlementStatus = 'missing' | 'provisional' | 'fallback' | 'partial_high' | 'partial' | 'final'

// 模拟computeDelta函数的核心逻辑
function shouldBlockComparison(status: SettlementStatus): boolean {
  return status === 'fallback' || status === 'partial'
}

describe('结算状态三级质量模型', () => {
  describe('状态屏蔽规则', () => {
    it('final 状态允许环比', () => {
      expect(shouldBlockComparison('final')).toBe(false)
    })

    it('partial_high 状态允许环比', () => {
      expect(shouldBlockComparison('partial_high')).toBe(false)
    })

    it('partial 状态屏蔽环比', () => {
      expect(shouldBlockComparison('partial')).toBe(true)
    })

    it('fallback 状态屏蔽环比', () => {
      expect(shouldBlockComparison('fallback')).toBe(true)
    })

    it('provisional 状态允许环比（但通常会因其他条件被屏蔽）', () => {
      expect(shouldBlockComparison('provisional')).toBe(false)
    })
  })

  describe('覆盖率场景映射', () => {
    interface CoverageScenario {
      collected: number
      expected: number
      expectedStatus: 'final' | 'partial_high' | 'partial'
    }

    const scenarios: CoverageScenario[] = [
      { collected: 22, expected: 22, expectedStatus: 'final' },
      { collected: 21, expected: 22, expectedStatus: 'partial_high' }, // 95.5%
      { collected: 20, expected: 22, expectedStatus: 'partial_high' }, // 90.9%
      { collected: 20, expected: 20, expectedStatus: 'final' },
      { collected: 18, expected: 20, expectedStatus: 'partial_high' }, // 90%
      { collected: 17, expected: 20, expectedStatus: 'partial' },    // 85%
      { collected: 89, expected: 100, expectedStatus: 'partial' },   // 89%
      { collected: 1, expected: 2, expectedStatus: 'partial' },      // 50%
    ]

    scenarios.forEach(({ collected, expected, expectedStatus }) => {
      const coverage = (collected / expected * 100).toFixed(1)
      it(`${collected}/${expected} (${coverage}%) -> ${expectedStatus}`, () => {
        const threshold = 0.90
        let actualStatus: 'final' | 'partial_high' | 'partial'

        if (collected === expected) {
          actualStatus = 'final'
        } else if (collected / expected >= threshold) {
          actualStatus = 'partial_high'
        } else {
          actualStatus = 'partial'
        }

        expect(actualStatus).toBe(expectedStatus)
      })
    })
  })

  describe('用户体验影响', () => {
    it('21/22 覆盖率应该不显示警告', () => {
      const coverage = 21 / 22
      const status = coverage >= 0.90 ? 'partial_high' : 'partial'
      const showWarning = shouldBlockComparison(status)

      expect(status).toBe('partial_high')
      expect(showWarning).toBe(false)
    })

    it('17/20 覆盖率应该显示警告', () => {
      const coverage = 17 / 20
      const status = coverage >= 0.90 ? 'partial_high' : 'partial'
      const showWarning = shouldBlockComparison(status)

      expect(status).toBe('partial')
      expect(showWarning).toBe(true)
    })
  })
})
