export type ThemePreference = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

export function resolveTheme(preference: ThemePreference, prefersDark: boolean): ResolvedTheme {
  return preference === 'system' ? (prefersDark ? 'dark' : 'light') : preference
}

export function applyResolvedTheme(theme: ResolvedTheme, root: Pick<HTMLElement, 'classList'> = document.documentElement) {
  root.classList.toggle('dark', theme === 'dark')
}

export function subscribeThemePreference(
  preference: ThemePreference,
  onTheme: (theme: ResolvedTheme) => void,
  media: MediaQueryList = window.matchMedia('(prefers-color-scheme: dark)'),
): () => void {
  const update = () => onTheme(resolveTheme(preference, media.matches))
  update()
  if (preference === 'system') media.addEventListener('change', update)
  return () => media.removeEventListener('change', update)
}
