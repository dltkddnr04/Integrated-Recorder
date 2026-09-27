import { describe, expect, it } from 'vitest'
import { resourceBreadcrumbs, resourceIdentity } from './resources'
import type { ResourceRef } from '@/types/api'

const chain: ResourceRef = { resource_type: 'gamma', resource_id: '3', parent: { resource_type: 'beta', resource_id: '2', parent: { resource_type: 'alpha', resource_id: '1' } } }

describe('opaque resource navigation', () => {
  it('returns the declared parent chain in root-to-current order', () => {
    expect(resourceBreadcrumbs(chain).map(resourceIdentity)).toEqual(['alpha / 1', 'beta / 2', 'gamma / 3'])
    expect(resourceBreadcrumbs(undefined)).toEqual([])
  })
})
