import { describe, expect, it } from 'vitest'
import { defaultValues, isVisible, validateSchema } from './schema'
import type { Schema } from '@/types/api'

describe('schema semantics', () => {
  const schema: Schema = { fields: [
    { key: 'enabled', control: 'boolean', label: 'Enabled', default: false },
    { key: 'token', control: 'secret', label: 'Token', required: true, visible_when: { field: 'enabled', truthy: true } },
    { key: 'quality', control: 'select', label: 'Quality', options: [{ value: { id: 1, variant: ['a'] }, label: 'A' }] },
    { key: 'formats', control: 'multi-select', label: 'Formats', constraints: { min_items: 1, max_items: 2 }, options: [{ value: 'a', label: 'A' }] },
  ] }

  it('applies defaults and evaluates declarative visibility', () => {
    const values = defaultValues(schema)
    expect(values.enabled).toBe(false)
    expect(isVisible(schema.fields[1].visible_when, values)).toBe(false)
    expect(isVisible(schema.fields[1].visible_when, { enabled: true })).toBe(true)
  })

  it('checks required secrets and item constraints without echoing values', () => {
    expect(validateSchema(schema, { enabled: true }, {}).token).toContain('필수')
    expect(validateSchema(schema, { enabled: false, formats: ['a', 'b', 'c'] }).formats).toContain('최대')
    expect(validateSchema(schema, { enabled: false, formats: ['a'] })).toEqual({})
  })

  it('rejects an explicitly cleared required default and non-finite numeric values', () => {
    const withDefaults: Schema = { fields: [
      { key: 'name', control: 'text', label: 'Name', required: true, default: 'preset' },
      { key: 'count', control: 'number', label: 'Count', required: true, default: 1 },
    ] }
    expect(validateSchema(withDefaults, { name: '', count: Number.NaN }).name).toContain('필수')
    expect(validateSchema(withDefaults, { name: 'preset', count: Number.NaN }).count).toContain('유효한 숫자')
    expect(validateSchema(withDefaults, { name: 'preset', count: 1 })).toEqual({})
  })

  it('rejects values outside declared select options using JSON value equality', () => {
    const selectSchema: Schema = { fields: [
      { key: 'mode', control: 'select', label: 'Mode', options: [{ value: { b: [2], a: 1 }, label: 'Declared' }] },
      { key: 'formats', control: 'multi-select', label: 'Formats', options: [{ value: 'webm', label: 'WebM' }] },
    ] }
    expect(validateSchema(selectSchema, { mode: { a: 1, b: [2] }, formats: ['webm'] })).toEqual({})
    expect(validateSchema(selectSchema, { mode: 'unknown', formats: ['webm', 'unexpected'] })).toEqual({ mode: '선택한 옵션이 올바르지 않습니다.', formats: '선택한 옵션이 올바르지 않습니다.' })
  })
})
