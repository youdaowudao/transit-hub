import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { highlightQuestionAnswer } from '@/modules/admin/utils/questionAnswers'

const cases: Array<{ id: string; answer: string; keywords: string[] | null; judgment: string; judgeable: boolean }> = JSON.parse(
  readFileSync(new URL('./fixtures/c1_keyword_cases.json', import.meta.url), 'utf8'),
)
describe('C1 keyword judgment and existing literal highlight contract', () => {
  for (const fixture of cases) {
    it(fixture.id, () => {
      const segments = highlightQuestionAnswer(fixture.answer, fixture.keywords ?? [])
      expect(segments.map(segment => segment.text).join('')).toBe(fixture.answer)
      expect(segments.some(segment => segment.highlighted)).toBe(fixture.judgment === 'correct')
      expect(segments.filter(segment => segment.highlighted).length).toBeLessThanOrEqual(3)
    })
  }
})
