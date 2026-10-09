import { expect, test } from 'vitest'
import { resolveReportSafetyScore } from './chatDisplay.ts'

test('resolveReportSafetyScore prefers a direct safety score, else derives it', () => {
  expect(resolveReportSafetyScore({
    cardType: 'report',
    directSafetyScore: 62.4,
    successCount: 3,
    failureCount: 2,
    executedCount: 5,
  })).toBe(62)

  expect(resolveReportSafetyScore({
    cardType: 'report',
    successCount: 3,
    failureCount: 2,
    executedCount: 5,
  })).toBe(undefined)

  expect(resolveReportSafetyScore({
    cardType: 'progress',
    riskScore: 20,
  })).toBe(80)
})
