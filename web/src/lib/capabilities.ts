import type { Adapter } from '@/types/api'

export function supportsCapability(adapter: Adapter | undefined, capability: string): boolean {
  return adapter?.descriptor?.capabilities?.includes(capability) ?? false
}
