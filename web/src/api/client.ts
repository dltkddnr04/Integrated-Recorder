export class APIError extends Error {
  readonly status: number
  readonly requestID?: string
  constructor(status: number, message: string, requestID?: string) {
    super(message); this.name = 'APIError'; this.status = status; this.requestID = requestID
  }
}

let csrfToken = ''
export function setCSRFToken(token: string | undefined) { csrfToken = token ?? '' }
export function invalidateCSRFToken() { csrfToken = '' }

type RequestOptions = Omit<RequestInit, 'body'> & { body?: unknown }
export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = (options.method ?? 'GET').toUpperCase()
  const headers = new Headers(options.headers)
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) headers.set('X-CSRF-Token', csrfToken)
  let response: Response
  try {
    response = await fetch(path, {
      ...options, method, headers, credentials: 'same-origin',
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    })
  } catch {
    throw new APIError(0, '서버에 연결할 수 없습니다. 네트워크 상태를 확인하세요.')
  }
  const contentType = response.headers.get('content-type') ?? ''
  const data: unknown = contentType.includes('json') ? await response.json().catch(() => null) : await response.text().catch(() => '')
  if (!response.ok) {
    if (response.status === 401 && path !== '/api/auth/session') window.dispatchEvent(new CustomEvent('ir:unauthorized'))
    const detail = typeof data === 'object' && data !== null && 'error' in data ? String((data as { error: unknown }).error) : `요청 실패 (${response.status})`
    throw new APIError(response.status, detail, response.headers.get('X-Request-ID') ?? undefined)
  }
  if (typeof data === 'object' && data !== null && 'csrf_token' in data) {
    const token = (data as { csrf_token?: unknown }).csrf_token
    if (typeof token === 'string') setCSRFToken(token)
  }
  return data as T
}

export function queryString(values: Record<string, string | number | boolean | undefined | null>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value !== undefined && value !== null && value !== '') params.set(key, String(value))
  const result = params.toString()
  return result ? `?${result}` : ''
}
