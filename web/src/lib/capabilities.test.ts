import { describe, expect, it } from 'vitest'
import { supportsCapability } from './capabilities'

describe('supportsCapability', () => {
  it('gates optional UI features on the adapter descriptor', () => {
    expect(supportsCapability(undefined, 'resource_browse')).toBe(false)
    expect(supportsCapability({ status: { id: 'x', state: 'ready' } }, 'resource_browse')).toBe(false)
    expect(supportsCapability({ status: { id: 'x', state: 'ready' }, descriptor: { capabilities: ['resolve', 'resource_browse'] } as never }, 'resource_browse')).toBe(true)
  })
})
