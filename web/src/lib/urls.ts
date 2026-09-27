/** Accepts only credential-free HTTP(S) destinations with a host. */
export function safeExternalURL(value: string): string {
  try {
    const url = new URL(value)
    if ((url.protocol !== 'https:' && url.protocol !== 'http:') || !url.hostname || url.username || url.password) return ''
    return url.toString()
  } catch {
    return ''
  }
}
