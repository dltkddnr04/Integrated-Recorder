import { APIError } from '@/api/client'

export function errorMessage(error: unknown): string {
  if (error instanceof APIError) return error.message || `요청 실패 (${error.status})`
  if (error instanceof Error) return error.message || '요청을 완료하지 못했습니다.'
  if (typeof error === 'string' && error.trim()) return error
  return '요청을 완료하지 못했습니다. 잠시 후 다시 시도하세요.'
}
