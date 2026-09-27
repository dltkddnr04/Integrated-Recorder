import { describe, expect, it, vi } from 'vitest'
import { applyResolvedTheme, resolveTheme, subscribeThemePreference } from './theme'

describe('theme preference', () => {
  it('resolves system preference from the operating system without changing the preference', () => {
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('system', false)).toBe('light')
    expect(localStorage.getItem('ir-theme')).toBeNull()
  })

  it('keeps explicit light and dark preferences independent from the operating system', () => {
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('dark', false)).toBe('dark')
  })

  it('applies the resolved class to the document root', () => {
    const root = document.createElement('div')
    applyResolvedTheme('dark', root)
    expect(root.classList.contains('dark')).toBe(true)
    applyResolvedTheme('light', root)
    expect(root.classList.contains('dark')).toBe(false)
  })

  it('tracks operating-system changes only for system preference', () => {
    let listener: ((event: MediaQueryListEvent) => void) | undefined
    const media = {
      matches: false,
      addEventListener: (_type: string, callback: (event: MediaQueryListEvent) => void) => { listener = callback },
      removeEventListener: (_type: string, callback: (event: MediaQueryListEvent) => void) => { if (listener === callback) listener = undefined },
    } as unknown as MediaQueryList
    const onTheme = vi.fn()
    const unsubscribe = subscribeThemePreference('system', onTheme, media)
    expect(onTheme).toHaveBeenLastCalledWith('light')
    ;(media as unknown as { matches: boolean }).matches = true
    listener?.({ matches: true } as MediaQueryListEvent)
    expect(onTheme).toHaveBeenLastCalledWith('dark')
    unsubscribe()
    expect(listener).toBeUndefined()
  })
})
