import type { ResourceRef } from '@/types/api'

export function resourceBreadcrumbs(resource?: ResourceRef): ResourceRef[] {
  if (!resource) return []
  const parents = resourceBreadcrumbs(resource.parent)
  return [...parents, resource]
}

export function resourceIdentity(resource: ResourceRef): string {
  return `${resource.resource_type} / ${resource.resource_id}`
}
