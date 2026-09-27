import type { Schema, SchemaField } from '@/types/api'

export function defaultValues(schema?: Schema): Record<string, unknown> {
  return Object.fromEntries((schema?.fields ?? []).filter(field => field.default !== undefined && field.control !== 'action' && field.control !== 'status').map(field => [field.key, field.default]))
}
function truthy(value: unknown) {
  if (Array.isArray(value)) return value.length > 0
  if (value && typeof value === 'object') return true
  return Boolean(value)
}
export function isVisible(condition: unknown, values: Record<string, unknown>): boolean {
  if (!condition || typeof condition !== 'object') return true
  const node = condition as Record<string, unknown>
  if (Array.isArray(node.all)) return node.all.every(child => isVisible(child, values))
  if (Array.isArray(node.any)) return node.any.some(child => isVisible(child, values))
  if (typeof node.field !== 'string') return true
  const value = values[node.field]
  if ('equals' in node) return JSON.stringify(value) === JSON.stringify(node.equals)
  if ('not_equals' in node) return JSON.stringify(value) !== JSON.stringify(node.not_equals)
  if (typeof node.truthy === 'boolean') return truthy(value) === node.truthy
  return true
}
export function validateSchema(schema: Schema, values: Record<string, unknown>, secrets: Record<string, string> = {}, checkRequired = true) {
  const errors: Record<string, string> = {}
  for (const field of schema.fields) {
    if (field.control === 'action' || field.control === 'status' || !isVisible(field.visible_when, values)) continue
    const value = field.control === 'secret' ? secrets[field.key] : values[field.key]
    const blank = value === undefined || value === null || value === '' || (Array.isArray(value) && value.length === 0)
    if (checkRequired && field.required && blank) { errors[field.key] = '필수 항목입니다.'; continue }
    if (blank) continue
    const c = field.constraints
    if (field.control === 'number' && typeof value === 'number') {
      if (!Number.isFinite(value)) { errors[field.key] = '유효한 숫자를 입력하세요.'; continue }
      if (c?.min !== undefined && value < c.min) errors[field.key] = `최솟값은 ${c.min}입니다.`
      if (c?.max !== undefined && value > c.max) errors[field.key] = `최댓값은 ${c.max}입니다.`
    }
    if (typeof value === 'string') {
      if (c?.min_length !== undefined && value.length < c.min_length) errors[field.key] = `최소 ${c.min_length}자 이상 입력하세요.`
      if (c?.max_length !== undefined && value.length > c.max_length) errors[field.key] = `최대 ${c.max_length}자까지 입력할 수 있습니다.`
      if (c?.pattern) { try { if (!new RegExp(c.pattern).test(value)) errors[field.key] = '형식이 올바르지 않습니다.' } catch { errors[field.key] = '이 입력 규칙을 확인할 수 없습니다.' } }
    }
    if (Array.isArray(value)) {
      if (c?.min_items !== undefined && value.length < c.min_items) errors[field.key] = `최소 ${c.min_items}개를 선택하세요.`
      if (c?.max_items !== undefined && value.length > c.max_items) errors[field.key] = `최대 ${c.max_items}개까지 선택할 수 있습니다.`
    }
  }
  return errors
}

export function fieldLabel(field: SchemaField) { return field.label || field.key }
