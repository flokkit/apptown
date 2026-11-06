import { expect, it } from 'vitest'
import { deploymentName } from './deploymentName'

it('shows explicit names and keeps generated preview keys out of headings', () => {
  expect(deploymentName({ kind: 'preview', name: 'preview-change-id', displayLabel: 'preview-change-id' })).toBe('Preview')
  expect(deploymentName({ kind: 'preview', name: 'preview-change-id', displayLabel: 'Try lunch' })).toBe('Try lunch')
  expect(deploymentName({ kind: 'standing', name: 'main', displayLabel: 'Production' })).toBe('Production')
})
